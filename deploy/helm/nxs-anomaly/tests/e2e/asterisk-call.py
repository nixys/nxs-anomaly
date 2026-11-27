#!/usr/bin/env python3
"""Run with Python 3, PyYAML, Helm and Docker. All call traffic stays on loopback."""
import base64
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time


def originate():
    for attempt in range(50):
        try:
            ami = socket.create_connection(("127.0.0.1", 5038), timeout=5)
            break
        except ConnectionRefusedError:
            if attempt == 49:
                raise
            time.sleep(0.2)

    with ami:
        ami.sendall(b"Action: Login\r\nUsername: oncall\r\nSecret: testami\r\nEvents: off\r\n\r\n"
                    b"Action: Originate\r\nActionID: nxs-originate\r\nChannel: PJSIP/+123@trunk\r\n"
                    b"Context: oncall\r\nExten: s\r\nPriority: 1\r\nCallerID: 123\r\nAsync: true\r\n\r\n"
                    b"Action: Logoff\r\n\r\n")
        with ami.makefile("rb") as stream:
            reply = stream.read()
        assert b"Originate successfully queued" in reply, reply


def respond(sock, peer, headers, address, body=""):
    dialog = [h for h in headers if h.startswith(("From:", "Via:", "Call-ID:", "CSeq:"))]
    to = next(h for h in headers if h.startswith("To:"))
    if ";tag=" not in to:
        to += ";tag=test"
    contact = (next(h for h in headers if h.startswith("Contact:")) if headers[0].startswith("REGISTER ")
               else f"Contact: <sip:test@{address}:5090>")
    response = ["SIP/2.0 200 OK", *dialog, to, contact]
    if body:
        response.append("Content-Type: application/sdp")
    response.extend([f"Content-Length: {len(body)}", "", body])
    sock.sendto("\r\n".join(response).encode(), peer)


def receive_calls(address, numbers, register):
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sip:
        sip.bind((address, 5090))
        sip.settimeout(180 if register else 10)
        if not register:
            originate()
        for index, number in enumerate(numbers):
            while True:
                packet, peer = sip.recvfrom(16384)
                headers = packet.decode().split("\r\n")
                method = headers[0].split()[0]
                if method == "REGISTER":
                    respond(sip, peer, headers, address)
                    print("REGISTER accepted", flush=True)
                    continue
                if method == "INVITE":
                    break
                if method in ("BYE", "OPTIONS"):
                    respond(sip, peer, headers, address)
            assert headers[0].startswith(f"INVITE sip:{number}@"), headers[0]
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as media:
                port = 15000 + index * 2
                media.bind((address, port))
                media.settimeout(10)
                sdp = (f"v=0\r\no=test 1 1 IN IP4 {address}\r\ns=test\r\nc=IN IP4 {address}\r\n"
                       f"t=0 0\r\nm=audio {port} RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
                respond(sip, peer, headers, address, sdp)
                audio = bytearray()
                while len(audio) < 16000:
                    packet, _ = media.recvfrom(2048)
                    assert packet[1] & 127 == 0
                    audio.extend(packet[12:])
                assert len(set(audio)) > 80, "missing speech in the RTP stream"
                print(f"Received speech for {number}", flush=True)


def run():
    import yaml  # Only the host needs PyYAML.
    chart = Path(__file__).resolve().parents[2]
    values = {"existingSecret": {"enabled": True, "name": "app"}, "asterisk": {
        "enabled": True, "existingSecret": "voice", "username": "test", "callerID": "123",
        "trunkHost": "127.0.0.1", "trunkPort": 5090, "register": False}}
    rendered = subprocess.check_output(["helm", "template", "test", str(chart), "-f", "-",
                                        "-s", "templates/asterisk.yaml", "-s", "templates/asterisk-config.yaml"],
                                       input=yaml.safe_dump(values).encode())
    objects = {obj["kind"]: obj for obj in yaml.safe_load_all(rendered)}
    config = objects["ConfigMap"]
    container = objects["Deployment"]["spec"]["template"]["spec"]["containers"][0]
    with tempfile.TemporaryDirectory() as directory:
        dest = Path(directory)
        dest.chmod(0o755)
        for name, content in config["data"].items():
            (dest / name).write_text(content)
        for name, encoded in config["binaryData"].items():
            (dest / name).write_bytes(base64.b64decode(encoded))
        cmd, *args = container["command"]
        cid = subprocess.check_output(["docker", "run", "-d", "--network", "none", "--read-only",
            "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", "1000:2000",
            "--tmpfs", "/run/asterisk:uid=1000,gid=2000", "-v", f"{dest}:/etc/asterisk:ro",
            "-e", "AMI_PASSWORD=testami", "-e", "SIP_PASSWORD=testsip", "--entrypoint", cmd,
            container["image"], *args], text=True).strip()
        try:
            subprocess.run(["docker", "exec", cid, "sh", "-ec",
                            'until [ -S /run/asterisk/asterisk.ctl ]; do sleep 0.2; done; exec asterisk -rx "core waitfullybooted"'],
                           timeout=20, check=True)
            subprocess.run(["docker", "exec", "-i", cid, "python3", "-", "--peer"],
                           input=Path(__file__).read_bytes(), check=True, timeout=30)
        finally:
            subprocess.run(["docker", "rm", "-f", cid], check=True)


if __name__ == "__main__":
    if sys.argv[1:] == ["--peer"]:
        receive_calls("127.0.0.1", ["+123"], False)
    elif sys.argv[1:] == ["--registrar"]:
        receive_calls(socket.gethostbyname(socket.gethostname()), ["+101", "+102"], True)
    else:
        run()

FROM python:3.14-alpine

RUN addgroup -S adapter && adduser -S -G adapter adapter \
    && mkdir /data && chown adapter:adapter /data
WORKDIR /app
COPY adapter.py .
USER adapter
EXPOSE 8080
VOLUME ["/data"]
CMD ["python", "-u", "adapter.py"]

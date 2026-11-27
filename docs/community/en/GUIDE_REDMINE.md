# Redmine issue creation

## Before you begin

Enable the Redmine REST API. Create a service account with permission to add issues to the target project and obtain its API key. Check that the worker can reach the Redmine base URL. [Installation options](INSTALLATION.md). [Install Redmine](https://www.redmine.org/projects/redmine/wiki/RedmineInstall).

## Connect and configure

1. Store the key in the environment variable named in `token_env` below.
2. Add this step to the escalation chain and replace project with your project identifier.
3. Supply the base URL; the adapter appends /issues.json.

```json
{"kind":"CREATE_ISSUE","tracker_type":"redmine","url":"https://redmine.example.org","project":"checkout","token_env":"REDMINE_API_KEY"}
```

## Verify and troubleshoot

Send one test alert. Check the delivery attempt and open the new issue in Redmine to verify its project, subject and description. For 403 check project permissions; for 422 check required tracker fields. Review both systems before replaying a failed request.

[Escalation and delivery configuration](CONFIGURATION.md) · [Integration catalogue](INTEGRATIONS.md)

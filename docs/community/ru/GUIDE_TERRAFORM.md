# Управление ресурсами через Terraform

## Перед началом

Установите Terraform на раннере. Получите базовый HTTPS URL API и ключ с нужной ролью. Для примера чтения достаточно viewer, для управления командами и цепочками нужны права записи. [Способы установки](INSTALLATION.md). [Установка CLI](https://developer.hashicorp.com/terraform/install).

## Подключение и настройка

1. Сохраните конфигурацию ниже в main.tf.
2. Передайте `NXS_ANOMALY_URL` и `NXS_ANOMALY_API_KEY` в окружение раннера.
3. Запустите terraform init, terraform validate и terraform plan.
4. Сохраните lock-файл и ограничьте доступ к state.

```hcl
terraform {
  required_providers {
    anomaly = { source = "nixys/nxs-anomaly" }
  }
}
provider "anomaly" {}
data "anomaly_teams" "all" {}
output "team_names" { value = data.anomaly_teams.all.teams[*].name }
```

## Проверка и устранение неполадок

Сверьте команды в плане с видимыми этой учётной записи в API. При 403 проверьте роль и область команды. Перед управлением существующими ресурсами импортируйте их, перед каждым apply просматривайте план. [Ресурсы провайдера](https://github.com/nixys/terraform-provider-nxs-anomaly).

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)

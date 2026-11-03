# Kladit Agent

Linux-агент для сбора и отправки метрик сервера в Kladit. CPU, RAM и uptime; без root при работе и без внешних Go-зависимостей.

Отправка по HTTPS реализована на стороне агента. Приём метрик и выдача machine-токенов в backend Kladit пока не реализованы. Контракт подключения: [Agent API v1](docs/PROTOCOL.md).

## Сборка

Требуется Go 1.25 или новее с актуальными исправлениями. Целевая система — Linux с procfs.

```sh
CGO_ENABLED=0 go build -o bin/kladit-agent ./cmd/kladit-agent
```

## Локальные метрики

```sh
./bin/kladit-agent metrics --policy examples/policy.json
```

Команда измеряет CPU за одну секунду и выводит JSON:

| Поле | Значение |
| --- | --- |
| `collected_at` | Время завершения сбора, UTC |
| `cpu_usage_percent` | Средняя загрузка всех CPU, 0–100%; idle и iowait исключены |
| `memory_total_bytes` | Общий объём RAM |
| `memory_available_bytes` | Доступная RAM, включая освобождаемый cache |
| `memory_used_bytes` | Общая RAM минус доступная |
| `uptime_seconds` | Время работы системы в секундах |

Память указана в байтах. Метрики относятся к procfs и не учитывают cgroup-лимиты контейнеров.

## Разрешения

Файл по умолчанию: `/etc/kladit-agent/policy.json`.

```json
{"version": 1, "capabilities": {"metrics.read": true}}
```

Отсутствующее разрешение и `false` запрещают сбор и отправку. Некорректная policy вызывает отказ. Поддерживается только `metrics.read`. Проверка конфигурации:

```sh
./bin/kladit-agent policy-check --policy examples/policy.json
```

## Отправка

`connection.json` содержит полный URL приёмника:

```json
{"endpoint": "https://api.example.com/api/v1/agent/metrics"}
```

Отдельный токен машины хранится в файле с правами `0600` или `0400`. Не используйте токен сессии пользователя, пароль SSH или общий секрет API.

```sh
./bin/kladit-agent send --policy examples/policy.json \
  --config /path/to/connection.json --token-file /path/to/token
```

Команда отправляет один снимок и завершается. Требуется HTTPS с доверенным сертификатом; redirects и прокси из environment отключены. Таймаут запроса — 10 секунд. Успех — HTTP 204, без вывода метрик или токена в stdout. Ошибки выводятся в stderr: код завершения `1`, неверные аргументы — `2`.

## Установка службы

Требуется Linux с systemd; для передачи токена через `LoadCredential` — systemd 247+. Установщик запускается с root, служба использует непривилегированный `DynamicUser`.

Локальный сбор каждые 30 секунд в journal:

```sh
sudo sh scripts/install.sh --allow-metrics
```

С отправкой в подготовленный HTTPS-приёмник:

```sh
sudo sh scripts/install.sh --allow-metrics \
  --config /path/to/connection.json --token-file /path/to/token
```

`--check` проверяет наличие файлов и системных компонентов без установки; это не проверка связи с приёмником. Установка сначала запускает сбор/отправку, затем включает таймер. При ошибке таймер не включается. Автоматическое обновление и rollback не поддерживаются; установка поверх существующих файлов запрещена.

Backend может запустить тот же установщик через SSH, передав проверенный комплект (`bin/`, `scripts/`, `packaging/`) и конфигурацию в закрытый каталог сервера. Код установщика запускается с правами root, поэтому комплект должен быть доверенным. Скачивание и проверка подписи релиза в установщик пока не входят.

```sh
systemctl status kladit-agent.timer
journalctl -u kladit-agent.service -n 20
```

Программа и policy принадлежат root. При отправке systemd передаёт root-only токен службе через credential-файл. Локальный режим не имеет сети; подключённый режим открывает только исходящее соединение и не слушает порт.

Для запрета метрик задайте `metrics.read: false`; policy перечитывается при каждом запуске, а при отправке — ещё раз после сбора. Для немедленной остановки:

```sh
sudo systemctl disable --now kladit-agent.timer
sudo systemctl stop kladit-agent.service
```

## Удаление

```sh
sudo sh scripts/uninstall.sh --confirm
```

Служба, бинарник и локальный токен удаляются. Policy, адрес приёмника и journal сохраняются. Копии токена также нужно отозвать на backend.

## Проверки

```sh
go test ./...
go vet ./...
sh -n scripts/install.sh scripts/uninstall.sh
```

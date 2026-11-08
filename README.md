# Kladit Agent

Linux-агент для сбора и отправки метрик сервера в Kladit. Собирает CPU, RAM, uptime, объём диска и показатели NVIDIA GPU. Работает без root и внешних Go-зависимостей.

Отправка по HTTPS реализована на стороне агента. Приём метрик и enrollment реализованы в Kladit; контракт требует совместимой версии backend. Контракт подключения: [Agent API v1](docs/PROTOCOL.md).

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
| `disk` | Общий, занятый и доступный объём файловой системы `/` |
| `gpus` | NVIDIA GPU с загрузкой, VRAM и температурой |

Объёмы указаны в байтах. CPU и RAM отражают данные procfs без учёта cgroup-лимитов. Для GPU нужен установленный драйвер и доступный службе `nvidia-smi`. Служба использует устройства группы `nvidia-frontend` с правами, заданными на хосте. Если GPU недоступен, в снимке будет `"gpus": []`. [Формат снимка](docs/PROTOCOL.md).

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
sudo sh /usr/local/lib/kladit-agent/uninstall.sh --confirm
```

Скрипт удаления сохраняется при установке, поэтому исходный комплект для удаления не нужен. Служба, бинарник, скрипт удаления и локальный токен удаляются. Policy, адрес приёмника и journal сохраняются. Для переустановки сразу используйте `--confirm --purge`, чтобы удалить policy и адрес приёмника. Пользовательские настройки systemd сохраняются и используются при следующей установке. Копии токена также нужно отозвать на backend.

## Проверки

```sh
go test ./...
go vet ./...
sh -n scripts/install.sh scripts/uninstall.sh
```

## Лицензия

Copyright (C) 2026 Егор Арефьев (Egorarefjev).

Kladit Agent распространяется по лицензии [GNU Affero General Public License, version 3](LICENSE) (`AGPL-3.0-only`).

Исходный код: [github.com/Egorarefjev/kladit-agent](https://github.com/Egorarefjev/kladit-agent).



## Установка из кабинета (enrollment)

Пользователь создаёт заявку в Kladit → сервер → «Агент» → «Установить вручную» и вставляет одну команду на Linux-сервере. Нужны sudo/root, curl, tar, sha256sum и systemd 247+. Go, git и ручные token/config файлы на сервере пользователя не нужны. Поддерживаются Linux amd64 и arm64. Повторная установка поверх существующей запрещена: сначала выполните штатный `sudo sh /usr/local/lib/kladit-agent/uninstall.sh --confirm --purge`, затем создайте новую заявку.

Команда разрешает только metrics.read. Enrollment живёт 10 минут; command/history/clipboard содержат этот временный секрет. Не публикуйте команду. После обмена он непригоден; machine token не выводится и остаётся только на машине. При потерянном ответе создайте новую заявку, автоматического повтора обмена нет.

### GitHub Releases

Оператору Kladit не нужны Go и локальная сборка. Для installer используйте URL конкретного опубликованного релиза:

```dotenv
PUBLIC_URL=https://kladit.example
AGENT_INSTALLER_URL=https://github.com/Egorarefjev/kladit-agent/releases/download/v0.2.1/install.sh
```

PUBLIC_URL — адрес Kladit, а installer и bundles скачиваются с GitHub Releases. Не используйте `/releases/latest/download/`. Backend должен поддерживать HTTPS-редиректы при скачивании installer; это не меняет запрет редиректов для enrollment и метрик.

### Создание релиза

Один раз включите **Settings → General → Releases → Enable release immutability** в GitHub-репозитории. Это блокирует замену файлов и перемещение тега опубликованного релиза. Одних versioned URL для такой защиты недостаточно. Workflow проверяет immutable-статус после публикации; без настройки релиз не считается успешно выпущенным.

1. Измените `const version` в `cmd/kladit-agent/main.go`, например на `0.3.0`, и слейте проверенный PR в main. Это единственный источник версии агента.
2. Создайте и отправьте соответствующий тег:

   ```sh
   git switch main
   git pull --ff-only
   git tag -a v0.3.0 -m "Kladit Agent v0.3.0"
   git push origin v0.3.0
   ```

Workflow запускается по тегу версии, проверяет gofmt, race tests, vet и shell-синтаксис, вызывает `package.sh`, проверяет контрольные суммы, версию бинарника и installer. Несовпадение тега и версии останавливает выпуск до создания Release.

Публикуются пять файлов:

- `kladit-agent-linux-amd64.tar.gz`
- `kladit-agent-linux-amd64.tar.gz.sha256`
- `kladit-agent-linux-arm64.tar.gz`
- `kladit-agent-linux-arm64.tar.gz.sha256`
- `install.sh`

Сначала создаётся draft и загружаются все файлы, затем релиз публикуется. Существующий релиз не перезаписывается; повторный запуск останавливается. Если загрузка оборвалась, удалите только незавершённый draft и повторите workflow. Для исправления опубликованного релиза выпускайте новый тег. Не перемещайте опубликованные теги.

Обычный push/PR CI работает отдельно и ничего не публикует. Новый workflow нужно сначала слить в main, затем создавать тег. Автоматического выпуска релизов при merge нет.

`scripts/package.sh` и `scripts/bootstrap.sh` остаются общим механизмом упаковки и установки. Checksums проверяют целостность архива; скачивание идёт по проверенному HTTPS, включая HTTPS-редиректы GitHub. Автообновление и SSH-доставка в этот flow не входят.

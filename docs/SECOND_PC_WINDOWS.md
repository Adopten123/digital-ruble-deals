# Проверка на втором ПК: Windows + Docker Desktop

Подготовлено 25 октября 2026 года по выбранной пользователем системе. **Фактический прогон на втором физическом ПК ещё не выполнен.** Linux CI и первый ПК его не заменяют. Используйте main после объединения PR #20.

## 1. Проверить среду

Нужны Git и запущенный Docker Desktop в режиме Linux-контейнеров. Go / Node.js / PostgreSQL на хосте не требуются. Для установки / неполадок WSL используйте [официальную инструкцию Docker Desktop для Windows](https://docs.docker.com/desktop/setup/install/windows-install/); настройки Windows / BIOS этим чек-листом автоматически не меняются.

В PowerShell; после ошибки сначала разберите текущий шаг, затем продолжайте:

```powershell
git --version
docker version
docker compose version
docker info --format '{{.OSType}}'
```

У docker version должны быть Client и Server, последний вывод — linux. Если Engine недоступен, сначала восстановите Docker Desktop; не считайте это результатом проверки приложения.

## 2. Получить чистую копию и отдельный проект

Выберите новую папку. git clone откажет, если она уже занята; существующий checkout с правками не очищайте. Для закрытого репозитория нужен аккаунт с доступом.

```powershell
git clone --branch main --single-branch https://github.com/Adopten123/digital-ruble-deals.git poaktu-judge-win
Set-Location poaktu-judge-win
git rev-parse HEAD
$demoProject = 'poaktu-win-' + (Get-Date -Format 'yyyyMMddHHmmss')
$env:POAKTU_PORT = '18080'
$env:POAKTU_ORIGIN = 'http://localhost:' + $env:POAKTU_PORT
docker compose -p $demoProject up --build --wait --wait-timeout 180
docker compose -p $demoProject ps --all
Invoke-RestMethod ($env:POAKTU_ORIGIN + '/readyz')
```

Сохраните значение demoProject: при перезапуске используйте **то же имя**. Оно отделяет новые контейнеры / volumes от прежних. Если порт занят, задайте другой POAKTU_PORT и соответствующий POAKTU_ORIGIN, затем повторите up для того же проекта. Ожидается status=ready, contour=test, stage=audit_integrity. Это готовность схемы, а не заключение A19.

Откройте http://localhost:18080 (либо выбранный порт). Учебная маркировка должна быть видна. Swagger — /api-docs/, схема 0.8.0, 37 операций. Базовые учётки и пароль — в [RUNNING.md](RUNNING.md).

## 3. Подготовить новый показ

Создайте частный файл. Код совместим с Windows PowerShell / PowerShell 7; CreateNew не заменяет существующий пароль. Файл остаётся в игнорируемой work. Docker Desktop должен иметь доступ к этому каталогу.

```powershell
$demoInput = Join-Path (Get-Location) 'work/demo-input'
New-Item -ItemType Directory -Path $demoInput -Force | Out-Null
$demoPasswordPath = Join-Path $demoInput 'password.txt'
$demoRandom = New-Object byte[] 24
$demoRng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $demoRng.GetBytes($demoRandom) } finally { $demoRng.Dispose() }
$demoPassword = [Convert]::ToBase64String($demoRandom)
$demoFile = [IO.File]::Open($demoPasswordPath, [IO.FileMode]::CreateNew)
try {
    $demoBytes = [Text.Encoding]::UTF8.GetBytes($demoPassword)
    $demoFile.Write($demoBytes, 0, $demoBytes.Length)
} finally { $demoFile.Dispose() }
$env:POAKTU_DEMO_INPUT_DIR = (Resolve-Path $demoInput).Path
docker compose -p $demoProject -f compose.yaml -f compose.demo.yaml run --rm --no-deps demo demo create --run-id win-check-001 --password-file /input/password.txt
```

Ожидается DEMO_READY, resumed=false, три identities и четыре deals. Повторите **только последнюю команду**, не генерацию файла: resumed=true и те же IDs. Исходный заказчик получает 500 000 ₽, исполнитель 0; все четыре сделки DRAFT. Пароль смотрите локально в своём частном файле. Правила повторов / ошибок / глобального оператора — [DEMO_RUN.md](DEMO_RUN.md).

## 4. Пройти приёмку

Открывайте роли в отдельных профилях браузера. Пользователь Windows, пароль, cookies и ключи в отчёт не нужны.

| Кейс | Проверка | Ожидается | Факт |
| --- | --- | --- | --- |
| WIN01 | Client / Server Docker, Linux Engine | Команды среды успешны | NOT RUN |
| WIN02 | Новый project / up / readyz / Swagger | Готовность, 37 операций, учебная маркировка | NOT RUN |
| WIN03 | Новый CLI и повтор | 3 роли / 4 DRAFT; повтор сохраняет IDs и деньги | NOT RUN |
| WIN04 | Вход трёх ролей | У сторон разные счета; оператор без своей организации | NOT RUN |
| WIN05 | payment: PDF, два согласия, резерв, первая подпись | PDF доступен участникам; одна подпись не платит | NOT RUN |
| WIN06 | Вторая подпись и доставка ЭДО | Один SETTLED / payment_id; заказчик 200 000 ₽, исполнитель 300 000 ₽, резерв 0 | NOT RUN |
| WIN07 | Тот же CLI после выплаты | Те же IDs / payment_id / текущие остатки; сброса нет | NOT RUN |
| WIN08 | Bootstrap / restart API и web / повторный вход | Выплата, история и прочитанность сохраняются | NOT RUN |
| WIN09 | Уведомления / CSV / JSON / чужой профиль | Доступ и содержимое соответствуют правам | NOT RUN |
| WIN10 | Обычный просмотр / скачивание PDF | Открывается фактический документ; отдельно проверить прежний R05 | NOT RUN |

Для WIN08:

```powershell
docker compose -p $demoProject run --rm bootstrap
docker compose -p $demoProject restart api web
docker compose -p $demoProject up -d --wait --wait-timeout 180 api web
Invoke-RestMethod ($env:POAKTU_ORIGIN + '/readyz')
docker compose -p $demoProject -f compose.yaml -f compose.demo.yaml run --rm --no-deps demo demo create --run-id win-check-001 --password-file /input/password.txt
```

Дополнительные ветки спора / отмены / недостатка средств — [DEMO.md](DEMO.md), zoom / устройства — [MANUAL_TESTING.md](MANUAL_TESTING.md). Для A19 используйте отдельный каталог эталона и процедуру остановки API из [INTEGRITY.md](INTEGRITY.md); readyz не подтверждает целостность. Реальный второй ПК ещё не отмечается PASS до получения фактов.

## 5. Сохранить результат

Сообщите commit из git rev-parse HEAD, версии Windows / Docker, выбранный project / порт и результаты WIN01–WIN10. При сбое достаточно конкретного сообщения ошибки и, при необходимости, последних строк:

```powershell
docker compose -p $demoProject logs --no-color --tail 80 api web migrate bootstrap
```

Не передавайте файл пароля, app.key, ключ A19 или состояние cookies. Для остановки с сохранением всех данных:

```powershell
docker compose -p $demoProject stop
```

Для продолжения запуска используйте прежний demoProject и up -d --wait. Проверка остановки не требует удаления volumes.

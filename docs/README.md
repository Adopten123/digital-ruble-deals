# Документация «ПоАкту»

Добавлен [новый демопоказ без сброса](DEMO_RUN.md): owner-only CLI, новые роли / счета, четыре DRAFT и безопасный повтор. [Отчёт с UI-снимками](reports/2026-10-25-isolated-demo-run.md); финальные CI / merge — PR #20. A19 уже объединён через PR #19, post-merge CI успешен. Второй пользовательский ПК остаётся открытым.


Тестовый B2B-прототип. Работают авторизация, организации, учебные остатки, версии условий / PDF, два согласия, доработка, резерв, тестовые подписи, проверяемая доставка ЭДО и однократная выплата, спор / двусторонняя отмена, история и CSV / JSON. В `feature/in-app-notifications` добавлены личные уведомления из outbox после commit: миграция 009, одинаковые права списка / счётчика / прочтения, сохранённый read_at и защита от повторов. Полный Go / PostgreSQL / vet / сборка, 71 unit и focused 17 API / UI пройдены; полный набор 148 E2E прошёл, после финальной правки отдельно 9 UI/setup; 10 ручных кейсов и smoke restart успешны. Финальный CI фиксируется в [STATUS.md](STATUS.md). A19 доступен отдельным [CLI](INTEGRITY.md) в feature/audit-integrity; локальная приёмка выполнена, окончательные CI / merge фиксируются в STATUS и PR #19. CLI нового демосценария и второй ПК остаются открытыми.

| Задача | Документ |
|---|---|
| Понять текущую готовность и решения | [STATUS.md](STATUS.md), [CONTEXT.md](CONTEXT.md) |
| Развернуть приложение и БД | [RUNNING.md](RUNNING.md) |
| Интегрировать действующий HTTP API | [Справочник API](api/README.md), [OpenAPI 3.0.3](api/openapi.json) |
| Открыть интерактивный Swagger | `/api-docs/` на origin запущенного приложения; инструкция в справочнике API |
| Изучить доставку личных уведомлений | [NOTIFICATIONS.md](NOTIFICATIONS.md) |
| Разрабатывать frontend | [FRONTEND.md](FRONTEND.md), [web/README.md](../web/README.md) |
| Проверить приложение | [TESTING.md](TESTING.md), [MANUAL_TESTING.md](MANUAL_TESTING.md) |
| Посмотреть отчёты последних этапов | [Уведомления](reports/2026-10-24-notifications.md), [история и экспорт](reports/2026-10-24-deal-history.md) |
| Продолжить работу по ТЗ | [HANDOFF.md](HANDOFF.md), [TZ.md](TZ.md), [BACKLOG.md](BACKLOG.md) |
| Изучить устройство backend и транзакций | [ARCHITECTURE.md](ARCHITECTURE.md), [DATA_MODEL.md](DATA_MODEL.md), [REFERENCE_ARCHITECTURE.md](REFERENCE_ARCHITECTURE.md) |
| Изучить общий контракт и состояния | [API.md](API.md), [STATE_MACHINE.md](STATE_MACHINE.md) |
| Понять продукт / демонстрацию | [PRODUCT.md](PRODUCT.md), [DEMO.md](DEMO.md), [SCENARIO.md](SCENARIO.md) |
| Проверить гипотезы / источники | [Исследования](research/README.md), [SOURCES.md](SOURCES.md), [ALTERNATIVES.md](ALTERNATIVES.md) |
| Работать совместно | [CONTRIBUTING.md](../CONTRIBUTING.md), [AGENTS.md](../AGENTS.md) |

OpenAPI 0.8.0 описывает все 37 зарегистрированных операций: 36 браузерных и одну внутреннюю с HMAC, недоступную через web-proxy. Готовность HTTP-сервера отмечена stage audit_integrity; версия схемы — 010_audit_integrity.sql. Проектные документы содержат весь требуемый продукт; их наличие не является подтверждением реализации. При изменении API синхронно обновлять Go, OpenAPI, справочник и TypeScript-клиент; проверять `npm --prefix web run docs:check`.

[Независимая проверка A19](INTEGRITY.md): покрытие, канонический формат, ключи / актуальность эталона, запуск и ограничения.

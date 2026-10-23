# Документация «ПоАкту»

Тестовый B2B-прототип. Работают авторизация, организации, учебные остатки, версии условий / PDF, два согласия, доработка до резерва, резервирование и учебный ledger оператора. Тестовые подписи, выплата, споры / освобождение, доставка уведомлений, экспорт и A19 остаются будущими этапами.

| Задача | Документ |
|---|---|
| Понять текущую готовность и решения | [STATUS.md](STATUS.md), [CONTEXT.md](CONTEXT.md) |
| Развернуть приложение и БД | [RUNNING.md](RUNNING.md) |
| Интегрировать действующий HTTP API | [Справочник API](api/README.md), [OpenAPI 3.0.3](api/openapi.json) |
| Открыть интерактивный Swagger | `/api-docs/` на origin запущенного приложения; инструкция в справочнике API |
| Разрабатывать frontend | [FRONTEND.md](FRONTEND.md), [web/README.md](../web/README.md) |
| Проверить приложение | [TESTING.md](TESTING.md), [MANUAL_TESTING.md](MANUAL_TESTING.md) |
| Продолжить работу по ТЗ | [HANDOFF.md](HANDOFF.md), [TZ.md](TZ.md), [BACKLOG.md](BACKLOG.md) |
| Изучить устройство backend и транзакций | [ARCHITECTURE.md](ARCHITECTURE.md), [DATA_MODEL.md](DATA_MODEL.md), [REFERENCE_ARCHITECTURE.md](REFERENCE_ARCHITECTURE.md) |
| Изучить целевой контракт ещё не реализованных функций | [API.md](API.md), [STATE_MACHINE.md](STATE_MACHINE.md) |
| Понять продукт / демонстрацию | [PRODUCT.md](PRODUCT.md), [DEMO.md](DEMO.md), [SCENARIO.md](SCENARIO.md) |
| Проверить гипотезы / источники | [Исследования](research/README.md), [SOURCES.md](SOURCES.md), [ALTERNATIVES.md](ALTERNATIVES.md) |
| Работать совместно | [CONTRIBUTING.md](../CONTRIBUTING.md), [AGENTS.md](../AGENTS.md) |

OpenAPI описывает только зарегистрированные публичные операции. Проектные документы содержат весь требуемый продукт; их наличие не является подтверждением реализации. При изменении API синхронно обновлять Go, OpenAPI, справочник и TypeScript-клиент; проверять `npm --prefix web run docs:check`.

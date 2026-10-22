# Frontend «ПоАкту»

React / TypeScript / Vite. Текущий этап: авторизация, организации, версии условий / PDF, два явных согласия и доработка до резерва. Денежный расчёт ещё не реализован.

- [Руководство frontend: запуск, компоненты, состояния, HTTP, хранение, стили и тесты](../docs/FRONTEND.md)
- [Реализованный API и примеры](../docs/api/README.md)
- [OpenAPI](../docs/api/openapi.json) — Swagger UI на `/api-docs/` запущенного web
- [Полный запуск с backend / БД](../docs/RUNNING.md)
- [Автоматические проверки](../docs/TESTING.md) и [ручная инструкция](../docs/MANUAL_TESTING.md)

Из корня репозитория: `npm --prefix web ci --ignore-scripts`, затем `npm --prefix web run dev`. Backend запускается отдельно; origin `http://localhost:5173`, proxy на `127.0.0.1:8081`. Swagger-assets готовятся автоматически перед dev/build. Не коммитить generated output или данные сессий.

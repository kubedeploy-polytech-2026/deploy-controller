# GitHub App

Установите App на `environment`, `backend` и `frontend` организации
`kubedeploy-polytech-2026`. Права: Contents read, Attestations read,
Deployments write. Webhook не требуется.

```bash
export GITHUB_APP_ID=123
export GITHUB_APP_INSTALLATION_ID=456
export GITHUB_APP_PRIVATE_KEY_FILE=/path/to/app.pem
./build/deploy-controller --execute --state-dir /path/to/state
```

Контроллер обновляет installation tokens перед истечением срока.
Для `environment` запрашивает Contents read и Deployments write;
для каждого приложения — Contents read и Attestations read.
Ключ перечитывается при обновлении токена. При ошибке App личный токен не используется.
Без этих переменных сохраняется локальная авторизация `gh`.

# Deployment request 1.0.0

Очередь — GitHub Deployments в `kubedeploy-polytech-2026/environment`.
Deployment создаётся с `task: deploy`, `environment: local-k3d`,
`production_environment: false`, `auto_merge: false`, `required_contexts: []`.
`ref` — полный commit окружения, уже доступный из `main`. Затем автор запроса
публикует status `queued`. Deployment без статуса не запускается.

`payload` — JSON-объект следующего вида. Значения `ENVIRONMENT_COMMIT` и
`EXPIRES_AT_RFC3339` заменяются при создании запроса. Показанный выбор приложений
соответствует опубликованным релизам `v0.1.0`:

```json
{
  "schema_version": "1.0.0",
  "environment_revision": "ENVIRONMENT_COMMIT",
  "expires_at": "EXPIRES_AT_RFC3339",
  "backend": {
    "tag": "v0.1.0",
    "revision": "858a5f6570eb2021799d2e46435551ac103cc127",
    "digest": "sha256:fbc9e444200ea810c4758de5a46070c3dd709f4080ee4424096a6243b84f769c"
  },
  "frontend": {
    "tag": "v0.1.0",
    "revision": "39cd6d7818b9de98d04cfeb43f24e13362004421",
    "digest": "sha256:a8fac82ba31481be408859d5ab3604bc193ff1447df9059c8310bfb43a3a5730"
  }
}
```

Полный SHA окружения должен совпадать с `sha` GitHub Deployment.
Срок действия — не более 24 часов от `created_at`; истечение проверяется
перед проверкой релизов и после неё. Рекомендуемый TTL запроса — один час.
Изменять системные часы для обработки просроченного запроса не нужно:
создаётся новый запрос с актуальным сроком действия.

Репозитории и registry path берутся из разрешённого набора контроллера:
`kubedeploy-polytech-2026/backend`, `kubedeploy-polytech-2026/frontend` и
соответствующие `ghcr.io/<repository>`. Payload не принимает произвольные
URL, имена репозиториев, shell-команды, image tags или пути к манифестам.
Backend и frontend могут выбирать разные версии.

Сейчас результат проверки существует только в stdout. Тестовый запрос после
проверки закрывается автором статусом `inactive`, поскольку ресурсы не
применялись. Контроллер не выставляет `success` за проверку плана.

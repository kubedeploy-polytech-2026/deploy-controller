# KubeDeploy Deploy Controller

Pull-контроллер пользовательского слоя KubeDeploy. Развивается отдельными
проверяемыми этапами: чтение deployment request, проверка релизов,
применение ресурсов и публикация результата.

Системным слоем управляет Argo CD из репозитория `platform`, описания приложений
находятся в `environment`. GitOps использует `main`.

## Текущий этап

Контроллер читает GitHub Deployments репозитория
`kubedeploy-polytech-2026/environment` для окружения `local-k3d` и проверяет
последний queued-запрос. Выводит JSON с неизменяемыми ссылками на образы backend
и frontend. Kubernetes и статусы GitHub не изменяются: `validated` означает
готовый план, применение и публикация результата добавляются следующим этапом.

Проверяются:

- версия payload, полный commit окружения и его принадлежность истории `main`;
- TTL до 24 часов, совпадение payload с SHA самого GitHub Deployment;
- опубликованный стабильный релиз и `release.json` в разрешённом репозитории;
- точное совпадение tag, commit, image и digest с выбором в запросе;
- объявленные платформы `linux/amd64` и `linux/arm64`, текущее положение тега;
- подписанный SLSA provenance образа через `gh attestation verify`: репозиторий,
  исходный commit, tag ref, workflow `.github/workflows/release.yml` и GitHub-hosted runner.

Верификация SBOM и повторное чтение отчётов Trivy в этот этап не входят.
Проверка provenance подтверждает образ по digest; список платформ здесь
проверяется по release metadata.

Из всех страниц API выбирается deployment с наибольшим ID. Если он уже завершён,
не queued, некорректен или просрочен, старые queued-запросы не возобновляются.
После удалённых проверок контроллер повторно читает последний deployment,
его статус и проверяет TTL. Ошибка API или верификатора останавливает текущую
проверку, без подстановки непроверенных значений.

## Запуск

Требования: Go 1.24+ и GitHub CLI с командами `api` и `attestation verify`
(проверено с `gh` 2.92.0). CI собирает Go 1.27.3.

```bash
make check
gh auth login --hostname github.com
./build/deploy-controller --once
./build/deploy-controller --interval 30s
```

Для локального этапа используется авторизация `gh`, включая `GH_TOKEN`, если
он задан. Нужен доступ на чтение contents, deployments и attestations исходных
репозиториев. Образы backend/frontend в GHCR публичные. Токены не записываются
в план и не передаются аргументами команд. Кластерная установка и GitHub App
появятся вместе с исполнением плана; пользовательский токен не устанавливается
в кластер.

Без queued-запроса результат — `{"state":"idle"}`. Успешная проверка выдаёт
`state: validated`, `deployment_id`, `environment_revision`, `backend_image`,
`frontend_image` и `expires_at`. Все image refs имеют вид `ghcr.io/...@sha256:...`.
`--once` возвращает ненулевой exit code при ошибке. В режиме polling ошибка
пишется в stderr, следующий опрос происходит после interval (минимум 10 секунд).
Один опрос ограничен тремя минутами; SIGINT/SIGTERM отменяет текущие команды.

Контроллер пока не забирает запрос из очереди: при повторном опросе тот же
queued-запрос проверяется снова. Несколько экземпляров могут одновременно
проверить один запрос. Это чтение; перед добавлением apply потребуются
сериализация и переходы статуса.

Формат запроса описан в [docs/request.md](docs/request.md). Ручная кнопка
создания запроса в Actions будет добавлена отдельным изменением.

## Проверка

`make check` запускает тесты с race detector, `go vet` и сборку. Тесты используют
подставной GitHub/CLI и не требуют токена, registry, Docker или Kubernetes.
Проверяются пагинация, актуальный статус, отбрасывание старых запросов,
неверные payload/release metadata, ошибка подписи, изменение запроса и TTL
во время верификации.

Контракт основан на [GitHub Deployments API](https://docs.github.com/en/rest/deployments/deployments),
[Deployment statuses API](https://docs.github.com/en/rest/deployments/statuses)
и [gh attestation verify](https://cli.github.com/manual/gh_attestation_verify).

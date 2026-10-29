# Релиз контроллера

После мерджа workflow в `main` релиз запускается тегом `vMAJOR.MINOR.PATCH`
на текущем commit `origin/main`. Тег и существующий GitHub Release не переиспользуются.

CI собирает и сканирует amd64/arm64, сохраняет образ, SBOM и Trivy report.
Publisher проверяет их hashes, Image ID, архитектуру и source labels, затем
публикует эти образы в `ghcr.io/kubedeploy-polytech-2026/deploy-controller`.
Релиз содержит `release.json` с digest общего index, SBOM и отчёты обеих архитектур.
Provenance подписан для index, SBOM — для каждого платформенного образа.

При первой публикации проверьте видимость пакета: для публичного pull она должна
быть Public. Установка в кластер использует digest из проверенного `release.json`.

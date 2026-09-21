# Free OpenCode API

用 Go 实现的轻量 API Gateway，参考
[anomalyco/opencode](https://github.com/anomalyco/opencode) 的 provider 设计，
将经过配置的免费模型以 OpenAI 兼容接口对外提供。

> 当前已实现 MVP 核心服务，正在继续完善部署与运营能力。

## 目标

- 提供 `GET /v1/models`；
- 提供 `POST /v1/chat/completions`；
- 支持普通 JSON 和 SSE 流式响应；
- 支持配置上游 HTTP/HTTPS 代理；
- 按 OpenCode 规则生成或透传每次请求所需的 session、request、client、project 和 User-Agent 请求头；
- 支持 OpenAI SDK、curl 以及其他兼容客户端；
- 通过 API Key 控制服务访问。

这里的“免费”依赖上游模型的免费额度或零成本政策。

## 目标使用方式

```bash
export SERVICE_API_KEY=change-me
go run ./cmd/server
```

调用接口：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $SERVICE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-free-model",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

## Docker 部署

GitHub Actions 会将镜像发布到：

```text
ghcr.io/noxmew/free-opencode-api:latest
```

首次发布后，需要在 GitHub Packages 中将该 Container package 设置为 Public；如果保持私有，部署机器需要先执行 `docker login ghcr.io`。

一键部署：

```bash
curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/deploy.sh | sh
```

脚本默认把文件下载到当前目录的 `free-opencode-api/`，首次运行会自动生成 `SERVICE_API_KEY`。也可以指定部署目录：

```bash
DEPLOY_DIR=/opt/free-opencode-api \
  sh -c 'curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/deploy.sh | sh'
```

手动部署：

```bash
curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/docker-compose.yml -o docker-compose.yml
curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/.env.example -o .env
# 修改 .env 中的 SERVICE_API_KEY
docker compose pull
docker compose up -d
```

## 参考

- [OpenCode](https://github.com/anomalyco/opencode)
- [OpenCode Zen 文档](https://opencode.ai/docs/zen)
- [OpenAI Chat Completions API](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/)

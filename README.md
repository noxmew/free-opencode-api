# Free OpenCode API

## 本地运行

```bash
go run ./cmd/server
```

调用接口：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mimo-v2.5-free",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

## Docker 部署

手动部署：

```bash
curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/docker-compose.yml -o docker-compose.yml
curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/.env.example -o .env
docker compose up -d
```

## 参考

- [OpenCode](https://github.com/anomalyco/opencode)
- [OpenCode Zen 文档](https://opencode.ai/docs/zen)
- [OpenAI Chat Completions API](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/)

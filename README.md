# Free OpenCode API

## 本地运行

```bash
go run ./cmd/server
```

## Chat Completions API

客户端传入的模型名如果没有以 `-free` 结尾，转发到上游时会自动补上该后缀。

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mimo-v2.5-free",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

## Responses API

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "muse-spark-1.3-contributor-free",
    "input": "你好"
  }'
```

## Docker 部署

手动部署：

```bash
curl -fsSL https://raw.githubusercontent.com/noxmew/free-opencode-api/main/docker-compose.yml -o docker-compose.yml
docker compose up -d
```

## 参考

- [OpenCode](https://github.com/anomalyco/opencode)
- [OpenCode Zen 文档](https://opencode.ai/docs/zen)

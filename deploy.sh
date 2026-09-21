#!/bin/sh

set -eu

REPO_RAW_BASE_URL="${REPO_RAW_BASE_URL:-https://raw.githubusercontent.com/noxmew/free-opencode-api/main}"
DEPLOY_DIR="${DEPLOY_DIR:-$PWD/free-opencode-api}"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yml"
ENV_FILE="$DEPLOY_DIR/.env"
ENV_EXAMPLE_FILE="$DEPLOY_DIR/.env.example"

if ! command -v docker >/dev/null 2>&1; then
	printf '%s\n' "docker is required" >&2
	exit 1
fi

if docker compose version >/dev/null 2>&1; then
	compose() {
		docker compose "$@"
	}
elif command -v docker-compose >/dev/null 2>&1; then
	compose() {
		docker-compose "$@"
	}
else
	printf '%s\n' "Docker Compose is required" >&2
	exit 1
fi

download() {
	url="$1"
	destination="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$destination"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$destination" "$url"
	else
		printf '%s\n' "curl or wget is required" >&2
		exit 1
	fi
}

mkdir -p "$DEPLOY_DIR"
download "$REPO_RAW_BASE_URL/docker-compose.yml" "$COMPOSE_FILE"
download "$REPO_RAW_BASE_URL/.env.example" "$ENV_EXAMPLE_FILE"

if [ ! -f "$ENV_FILE" ]; then
	if command -v openssl >/dev/null 2>&1; then
		api_key=$(openssl rand -hex 32)
	else
		api_key=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
	fi
	sed "s/^SERVICE_API_KEY=.*/SERVICE_API_KEY=$api_key/" "$ENV_EXAMPLE_FILE" > "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	printf '%s\n' "Generated SERVICE_API_KEY and saved it to $ENV_FILE"
fi

compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" pull
compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" up -d
compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" ps

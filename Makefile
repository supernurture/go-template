codegen:
	bash scripts/codegen.sh
queriesgen:
	go tool sqlc generate -f internal/infrastructure/database/sqlc.yaml
oapicodegen:
	bash scripts/oapicodegen.sh
sqlcgen:
	go tool sqlc generate -f internal/infrastructure/database/sqlc.yaml
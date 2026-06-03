oapicodegen:
	bash scripts/oapicodegen.sh
sqlcodegen:
	go tool sqlc generate -f internal/infrastructure/database/sqlc.yaml
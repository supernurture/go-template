codegen:
	bash scripts/codegen.sh

SQLC_DIR=internal/infrastructure/database
.PHONY: querygen
querygen:
	go tool sqlc generate -f $(SQLC_DIR)/sqlc.yaml
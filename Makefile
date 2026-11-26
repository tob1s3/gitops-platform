.PHONY: test validate image up down argocd-ui

test:            ## тесты сервиса
	cd app && go vet ./... && go test -race -count=1 ./...

validate:        ## статическая проверка чартов и terraform
	scripts/validate-chart.sh
	cd terraform && terraform fmt -check -recursive && terraform init -backend=false -input=false >/dev/null && terraform validate

image:           ## собрать образ локально
	docker build -t pulse:dev --build-arg VERSION=dev app

up: image        ## kind + Argo CD + приложения из Git (нужен Docker)
	cd terraform && terraform init -input=false && terraform apply -auto-approve \
	  -var 'local_images=["pulse:dev"]' \
	  -var 'image_override={repository="pulse",tag="dev",pullPolicy="Never"}'

down:            ## удалить стенд
	cd terraform && terraform destroy -auto-approve

argocd-ui:       ## открыть Argo CD на http://localhost:8080
	@echo "login: admin, password: $$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)"
	kubectl -n argocd port-forward svc/argocd-server 8080:80

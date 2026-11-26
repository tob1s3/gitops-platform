output "kubeconfig_path" {
  description = "Путь к kubeconfig созданного кластера."
  value       = kind_cluster.this.kubeconfig_path
}

output "kube_context" {
  description = "Контекст kubectl."
  value       = kind_cluster.this.context_name
}

output "argocd_ui" {
  description = "Как открыть интерфейс Argo CD."
  value       = "kubectl -n argocd port-forward svc/argocd-server 8080:80, пароль admin: kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d"
}

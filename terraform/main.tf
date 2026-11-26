# Локальная платформа: kind-кластер → Argo CD → корневое приложение (app of apps).
# Дальше всё, что лежит в кластере, Argo CD берёт из Git: terraform больше ничего не деплоит.

resource "kind_cluster" "this" {
  name           = var.cluster_name
  node_image     = var.node_image
  wait_for_ready = true

  kind_config {
    kind        = "Cluster"
    api_version = "kind.x-k8s.io/v1alpha4"

    node {
      role = "control-plane"
    }

    dynamic "node" {
      for_each = range(var.worker_count)
      content {
        role = "worker"
      }
    }
  }
}

resource "kind_load" "images" {
  for_each = toset(var.local_images)

  image        = each.value
  cluster_name = kind_cluster.this.name
}

provider "helm" {
  kubernetes = {
    host                   = kind_cluster.this.endpoint
    cluster_ca_certificate = kind_cluster.this.cluster_ca_certificate
    client_certificate     = kind_cluster.this.client_certificate
    client_key             = kind_cluster.this.client_key
  }
}

resource "helm_release" "argocd" {
  name             = "argocd"
  repository       = "https://argoproj.github.io/argo-helm"
  chart            = "argo-cd"
  version          = var.argocd_chart_version
  namespace        = "argocd"
  create_namespace = true
  wait             = true
  timeout          = 600

  values = [yamlencode({
    # Локальный стенд: без SSO и уведомлений, UI доступен через port-forward.
    dex           = { enabled = false }
    notifications = { enabled = false }
    configs = {
      params = {
        "server.insecure" = true
      }
      cm = {
        # Проверять репозиторий раз в минуту вместо трёх: на демо изменения видны быстрее.
        "timeout.reconciliation" = "60s"
      }
    }
  })]
}

# Корневое приложение смотрит на папку gitops/ (чарт platform-apps), а тот создаёт
# AppProject и по одному Application на окружение.
resource "helm_release" "root_app" {
  name       = "root-app"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argocd-apps"
  version    = var.argocd_apps_chart_version
  namespace  = helm_release.argocd.namespace

  values = [yamlencode({
    applications = {
      platform = {
        namespace  = helm_release.argocd.namespace
        project    = "default"
        finalizers = ["resources-finalizer.argocd.argoproj.io"]
        source = {
          repoURL        = var.repo_url
          targetRevision = var.git_revision
          path           = "gitops"
          helm = {
            valuesObject = merge(
              {
                repoURL        = var.repo_url
                targetRevision = var.git_revision
              },
              length(local.image_override) > 0 ? { imageOverride = local.image_override } : {}
            )
          }
        }
        destination = {
          server    = "https://kubernetes.default.svc"
          namespace = helm_release.argocd.namespace
        }
        syncPolicy = {
          automated = {
            prune    = true
            selfHeal = true
          }
        }
      }
    }
  })]

  # Образы должны оказаться на узлах раньше, чем Argo CD создаст поды.
  depends_on = [kind_load.images]
}

locals {
  image_override = { for k, v in var.image_override : k => v if v != null }
}

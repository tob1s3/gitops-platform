variable "cluster_name" {
  description = "Имя kind-кластера."
  type        = string
  default     = "gitops-platform"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,40}$", var.cluster_name))
    error_message = "Только строчные латинские буквы, цифры и дефис, до 41 символа."
  }
}

variable "node_image" {
  description = "Образ узлов kind (kindest/node:vX.Y.Z). null — версия по умолчанию для kind."
  type        = string
  default     = null
}

variable "worker_count" {
  description = "Число worker-узлов. Нужно хотя бы 2, чтобы проверить разнесение реплик (topologySpread) и PDB."
  type        = number
  default     = 2

  validation {
    condition     = var.worker_count >= 1 && var.worker_count <= 5
    error_message = "От 1 до 5 worker-узлов."
  }
}

variable "argocd_chart_version" {
  description = "Версия Helm-чарта argo-cd (https://github.com/argoproj/argo-helm)."
  type        = string
  default     = "10.20.0"
}

variable "argocd_apps_chart_version" {
  description = "Версия Helm-чарта argocd-apps."
  type        = string
  default     = "2.0.10"
}

variable "repo_url" {
  description = "Git-репозиторий, из которого Argo CD берёт манифесты. Должен быть публичным или доступным Argo CD."
  type        = string
  default     = "https://github.com/tob1s3/gitops-platform.git"
}

variable "git_revision" {
  description = "Ветка, тег или SHA коммита. В CI — SHA проверяемого коммита."
  type        = string
  default     = "main"
}

variable "local_images" {
  description = "Локально собранные образы, которые нужно загрузить в узлы kind (например, [\"pulse:e2e\"])."
  type        = list(string)
  default     = []
}

variable "image_override" {
  description = "Подмена образа во всех окружениях (для e2e). Пустой объект — использовать значения из environments/."
  type = object({
    repository = optional(string)
    tag        = optional(string)
    pullPolicy = optional(string)
  })
  default = {}
}

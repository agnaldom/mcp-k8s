# mcp-k8s — Especificação v0.1

**Status:** Draft
**Repositório:** `mcp-k8s`
**Binário:** `mcp-k8s`
**Linguagem:** Go
**Escopo v0.1:** somente leitura · kubeconfig + in-cluster · transporte stdio
**Consumidores:** `srectl` (primeiro), depois Claude Code/Desktop, Codex e `sre-agent`

---

## 1. O que este servidor é

Um fornecedor de **fatos Kubernetes estruturados**, com limites, sanitização e auditoria.

Ele não raciocina, não correlaciona causa, não recomenda ação. Quem faz isso é o
consumidor — a CLI mostra para um humano decidir, o agente futuro monta hipótese.

O critério para incluir qualquer campo na resposta: *isso é lido diretamente da API do
Kubernetes ou derivado dela por uma regra determinística e declarada?* Se a resposta é
não, não entra.

## 2. O que fica de fora do v0.1

| Fora | Por quê |
|---|---|
| Qualquer mutação | `create`, `patch`, `delete`, `scale`, `exec`, `port-forward`, `attach` |
| Rancher provider | v0.2 — maior superfície de credencial do projeto, entra depois do contrato estar validado |
| Transporte HTTP | v0.2 — traz auth, CORS, DNS rebinding, ciclo de sessão. Todos os consumidores do v0.1 falam stdio |
| `watch` / streaming | v0.2 |
| Conteúdo de Secret | permanentemente bloqueado por policy, mesmo com RBAC permitindo |

Não existe flag `--allow-write` escondida. Quando houver escrita, será **outro binário**
com outra ServiceAccount e outro RBAC — um bug de configuração não pode transformar um
servidor de leitura em administrador.

---

## 3. Convenções de contrato

### 3.1 Nomes de tool usam underscore

```
k8s_pod_logs        ✓
k8s.pod.logs        ✗
```

Nomes de function na API da OpenAI seguem `^[a-zA-Z0-9_-]{1,64}$` — ponto não é
permitido, e o mesmo vale em provedores compatíveis. O MCP não especifica formato
(SEP-986 segue aberta), então a interseção segura é underscore. Prefixo `k8s_` evita
colisão quando `mcp-scm` e `mcp-kb` entrarem na mesma sessão.

### 3.2 Envelope

```json
{
  "apiVersion": "mcp-k8s/v1",
  "cluster": { "id": "...", "name": "prod" },
  "data": {},
  "coverage": { "from": "...", "to": "...", "complete": true },
  "meta": { "timestamp": "...", "durationMs": 81, "view": "summary" }
}
```

### 3.3 `coverage` — o campo que quase ninguém coloca e que decide tudo

Toda tool que devolve dado limitado no tempo (eventos, logs, métricas) **declara qual
janela ela comprovadamente cobre**.

Motivo concreto: o `--event-ttl` do kube-apiserver tem default de 1 hora, e em cluster
gerenciado você normalmente não controla essa flag. Num incidente de duas horas, os
eventos anteriores ao rollout simplesmente não existem mais.

Sem `coverage`, o consumidor não tem como distinguir *"não houve falha antes do
rollout"* de *"não temos dado sobre antes do rollout"*. As duas chegam como lista
vazia. Isso faz um agente confirmar a hipótese com mais frequência quanto **mais
velho** for o incidente — viés de confirmação implementado em código.

```json
"coverage": {
  "from": "2026-09-09T14:58:00Z",
  "to":   "2026-09-09T15:31:00Z",
  "complete": false,
  "reason": "oldest available event is newer than requested window (likely --event-ttl)"
}
```

Regra: `complete: false` obriga o consumidor a tratar ausência como desconhecido.

### 3.4 `view` — projeção de campos

O consumidor é um terminal ou um modelo com janela de contexto. Objeto Kubernetes cru
é caro e majoritariamente inútil.

| `view` | Conteúdo |
|---|---|
| `summary` (default) | campos de identidade + estado + o que muda durante incidente |
| `full` | objeto normalizado completo, ainda sanitizado |

**Sempre removido, nos dois modos:**

- `metadata.managedFields` — frequentemente maior que o `spec` e sem valor operacional
- `metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"]`
- annotations acima de 4 KiB (substituídas por `"[TRUNCATED: 12847 bytes]"`)

### 3.5 Truncamento

Dois mecanismos distintos que não devem ser confundidos:

| Situação | Comportamento |
|---|---|
| Lista maior que o limite | pagina normalmente, devolve `pagination.continue` |
| Logs maiores que o limite | trunca **no início**, devolve `meta.truncated: true` e quantas linhas foram descartadas |
| **Objeto único** maior que `response.maxBytes` | **erro `RESPONSE_TOO_LARGE`**, nunca truncado |

Um objeto JSON cortado no meio é pior que um erro: o consumidor recebe estrutura
inválida ou, pior, estrutura válida com conteúdo faltando silenciosamente. A saída é
pedir `view: summary` ou paginar — o erro diz isso na mensagem.

### 3.6 Erros

```json
{ "apiVersion": "mcp-k8s/v1",
  "error": { "code": "K8S_FORBIDDEN", "message": "...", "retryable": false } }
```

```
INVALID_ARGUMENT · CLUSTER_NOT_FOUND · CLUSTER_UNAVAILABLE · CLUSTER_AUTH_FAILED
NAMESPACE_NOT_FOUND · RESOURCE_NOT_FOUND · RESOURCE_TYPE_NOT_FOUND
K8S_FORBIDDEN · K8S_UNAUTHORIZED · K8S_TIMEOUT · K8S_API_ERROR
POLICY_DENIED · METRICS_UNAVAILABLE · LOGS_UNAVAILABLE
RESPONSE_TOO_LARGE · RATE_LIMITED · INTERNAL_ERROR
```

`METRICS_UNAVAILABLE` (sem metrics-server) e `LOGS_UNAVAILABLE` não são falhas do
servidor — são estados normais de cluster e o consumidor deve seguir sem eles.

---

## 4. Catálogo de tools v0.1

Doze tools. Duas delas é que justificam o projeto existir.

| Tool | Papel |
|---|---|
| `k8s_cluster_list` | clusters/contexts disponíveis, com qual é o default |
| `k8s_cluster_info` | versão do Kubernetes e capabilities detectadas |
| `k8s_namespace_list` | namespaces após policy |
| `k8s_api_resources` | discovery **com permissões efetivas** |
| `k8s_resource_list` | qualquer Kind, via dynamic client |
| `k8s_resource_get` | qualquer Kind, com `view` |
| `k8s_pod_logs` | logs, incluindo `previous` |
| `k8s_events_list` | eventos normalizados, com `coverage` |
| `k8s_metrics_pods` | consumo por pod |
| `k8s_metrics_nodes` | consumo por node |
| **`k8s_workload_context`** | **o bundle de troubleshooting numa chamada** |
| **`k8s_signals`** | **sinais determinísticos com threshold declarado** |

**Não existe `k8s_context_list` nem `k8s_context_current`.** Com o provider de
kubeconfig, cada context *é* um cluster: `k8s_cluster_list` devolve todos com
`"default": true` em um deles. Duas tools a menos e um conceito a menos para o
consumidor entender.

**Não existe `k8s_resource_describe`.** Tinha ~80% de sobreposição com
`k8s_workload_context` — duas tools quase iguais fazem o consumidor escolher errado e
você mantém as duas. Sobrou uma.

**Nenhuma tool aceita "cluster atual" implícito.** Toda chamada recebe `cluster`
explícito e resolve o próprio `rest.Config`. Não existe estado global mutável de
contexto no servidor. Isso é o que impede que uma sessão troque silenciosamente o
cluster de outra.

---

## 5. Tools que definem o projeto

### 5.1 `k8s_workload_context`

Uma chamada, todo o contexto de um workload. É a diferença entre este servidor e um
wrapper de API.

**Input**

```json
{ "cluster": "prod", "namespace": "payments",
  "kind": "Deployment", "name": "payments-api",
  "includeMetrics": true, "eventLimit": 50 }
```

`kind` aceita `Pod`, `Deployment`, `StatefulSet`, `DaemonSet`, `Job`, `CronJob`.

**Output**

```
workload          estado, conditions, replicas
pods[]            fase, containers, estados atual e anterior, restartCount,
                  requests/limits, node
relationships     ownerReference chain, Services, EndpointSlices, HPA, PDB, PVC
events[]          normalizados, com coverage
metrics           por container, quando disponível
signals[]         ver 5.2
coverage          janela coberta por evento e por métrica
```

**Não inclui logs.** Logs custam ordens de magnitude mais que metadata e a decisão de
qual pod ler pertence a quem chamou. `k8s_pod_logs` existe para isso, e o
`workload_context` diz **quais pods** vale a pena ler.

**Coleta paralela com concorrência limitada:** máximo 8 requisições ao Kubernetes por
invocação. Falha parcial não derruba a resposta — o campo que falhou vem com
`"unavailable": "<motivo>"` e o resto é entregue.

**Orçamento:** `workload_context` de um Deployment com 20 pods pode ficar grande.
Limite de pods detalhados: 20 por default (`maxPods`), os demais aparecem só no sumário
com contagem por fase.

### 5.2 `k8s_signals` — e por que ele não viola a regra dos fatos

Um sinal parece julgamento e não pode ser. A solução é que **cada sinal carrega a
regra que o disparou e o valor observado**:

```json
{
  "type": "high_restart_count",
  "severity": "warning",
  "rule": { "expression": "restartCount >= 10", "window": "1h", "configurable": true },
  "observed": { "restartCount": 17 },
  "source": "pod/payments-api-89abc .status.containerStatuses[api].restartCount"
}
```

Isso é fato — `restartCount = 17` — mais uma regra **declarada e configurável**. O
consumidor pode discordar do threshold porque ele está na resposta. Um campo
`"high_restart_count": true` sozinho seria opinião disfarçada de dado.

Catálogo v0.1, com defaults:

| Sinal | Regra default | Configurável |
|---|---|---|
| `container_oom_killed` | `lastState.terminated.reason == OOMKilled` | não (é fato puro) |
| `container_crash_loop` | `waiting.reason == CrashLoopBackOff` | não |
| `container_image_pull_error` | `waiting.reason in {ImagePullBackOff, ErrImagePull}` | não |
| `container_not_ready` | `ready == false` por ≥ 5 min | sim |
| `high_restart_count` | `restartCount >= 10` em 1h | sim |
| `pod_unschedulable` | `condition PodScheduled=False` | não |
| `pod_evicted` | `status.reason == Evicted` | não |
| `workload_unavailable` | `available < desired` | não |
| `service_without_endpoints` | `readyEndpoints == 0` com selector não vazio | não |
| `pvc_pending` | `phase == Pending` por ≥ 2 min | sim |
| `node_not_ready` | `condition Ready != True` | não |
| `memory_near_limit` | `usage / limit >= 0.90` | sim |

Sinais com `configurable: false` são leitura direta de campo. Os configuráveis vêm de
`signals:` no config e o valor efetivo sempre aparece em `rule`.

### 5.3 `k8s_api_resources` — permissões efetivas, não capabilities

Devolver `verbs: [get, list, watch]` do discovery informa o que **o Kubernetes**
suporta, não o que **você** pode fazer. Um consumidor lê "posso listar", planeja em
cima disso e toma 403.

O v0.1 resolve com `SelfSubjectAccessReview`:

```json
{ "group": "apps", "version": "v1", "resource": "deployments",
  "kind": "Deployment", "namespaced": true,
  "verbs": ["get","list","watch"],
  "allowed": { "get": true, "list": true, "watch": false },
  "policyBlocked": false }
```

`verbs` = capability do cluster. `allowed` = o que esta identidade pode de fato.
`policyBlocked` = negado pela policy local antes mesmo do RBAC.

Batched e cacheado por 5 minutos junto do discovery.

### 5.4 `k8s_pod_logs`

```json
{ "cluster": "prod", "namespace": "payments", "pod": "payments-api-89abc",
  "container": "api", "previous": false, "since": "30m",
  "tailLines": 500, "timestamps": true }
```

- **Um container:** seleção automática. **Vários:** `container` obrigatório ou
  `allContainers: true`. Nunca escolher arbitrariamente.
- `previous: true` é obrigatório funcionar — é o único jeito de ver o que aconteceu
  antes de um `CrashLoopBackOff` ou `OOMKilled`.
- Defaults: `tailLines: 500`, `timestamps: true`.
  Máximos: `tailLines: 5000`, `maxBytes: 1 MiB`.
- Truncamento corta o **início** e reporta `meta.droppedLines`.

**Sobre os máximos:** 1 MiB de log são grosso modo 250 mil tokens. Nenhum modelo
consome isso e nenhum humano lê. O máximo existe para conter dano, não para ser usado —
o default é o número que importa.

---

## 6. Sanitização — o que é controle e o que não é

Duas categorias, e confundi-las é como se cria falsa confiança.

### 6.1 Controles de verdade (determinísticos, testáveis)

| Regra | Garantia |
|---|---|
| `Secret` bloqueado por policy | mesmo com RBAC permitindo. `Secret.data`/`stringData` nunca são serializados — nem em `workload_context`, nem em `full` |
| `env[].value` → `[REDACTED]` | valor literal em spec de container nunca sai |
| `env[].valueFrom.secretKeyRef` | devolve nome do secret e da chave, nunca resolve o valor |
| `managedFields`, `last-applied-configuration` | removidos sempre |
| kubeconfig, token, certificado | nunca em resposta, nunca em log |
| exec credential plugins | desabilitados por default (`allowExecPlugins: false`) — eles executam processo local |

Estes são cobertos por testes de regressão de segurança que rodam em todo PR.

### 6.2 Defesa em profundidade (best-effort, e a spec diz isso)

Redação de segredo dentro de **conteúdo de log** por regex: `Bearer`, JWT, chave
privada PEM, connection string, chave de cloud.

**Isto não é um controle.** Log de aplicação é texto arbitrário e a taxa de falso
negativo é alta e não mensurável. Chamar isso de controle produz confiança falsa e não
sobrevive a uma auditoria séria.

O controle real é RBAC: não conceder `pods/log` amplamente. A redação reduz dano
quando o log já foi lido — não autoriza lê-lo.

O `README` e o threat model devem dizer isso com estas palavras. Prometer redação
confiável de log é o tipo de afirmação que destrói a credibilidade do projeto inteiro
quando alguém demonstra o contrário em cinco minutos.

---

## 7. Policy

```yaml
security:
  readonly: true
  clusters:   { allow: [] }              # vazio = todos
  namespaces: { allow: [], deny: [kube-system, cattle-system] }
  resources:  { deny: [Secret, TokenRequest] }
  kubeconfig: { allowExecPlugins: false }
```

Ordem de avaliação, sempre nesta sequência:

```
schema do input → policy local → resolução de cluster → RBAC do Kubernetes → API
```

A policy local nunca é dispensada porque "o RBAC já cobre". São camadas independentes:
RBAC é do cluster e muda sem você saber; a policy é sua e é versionada.

---

## 8. Limites

```yaml
limits:
  requestTimeout: 30s
  response:  { maxBytes: 4194304 }
  list:      { defaultLimit: 100, maxLimit: 500 }
  logs:      { defaultTailLines: 500, maxTailLines: 5000, maxBytes: 1048576 }
  events:    { maxItems: 500 }
  workload:  { maxPods: 20, maxConcurrentK8sRequests: 8 }
kubernetes:
  qps: 20
  burst: 40
```

Timeouts em cascata: tool 30s · request Kubernetes 15s · logs 20s · métricas 10s.
Cancelamento do MCP propaga por `context.Context` até o `client-go`.

---

## 9. Cache

| O quê | TTL | Onde |
|---|---|---|
| discovery + RESTMapper | 5 min | **disco** (`~/.cache/mcp-k8s/`) e memória |
| lista de clusters | 5 min | memória |
| descoberta de metrics API | 5 min | memória |

O cache em disco não é otimização opcional: a `srectl` sobe o servidor como subprocesso
a cada comando. Sem discovery persistido, cada `srectl get pods` paga 1–2 segundos de
discovery. Com cache em disco, o segundo comando é instantâneo.

Permissões: diretório `0700`, arquivos `0600`. Cache é por cluster e o conteúdo nunca
vai para log.

---

## 10. Auditoria

Toda invocação, mesmo de leitura:

```json
{ "timestamp": "...", "requestId": "...", "tool": "k8s_pod_logs",
  "cluster": "prod", "namespace": "payments", "resource": "payments-api-89abc",
  "result": "success", "durationMs": 124, "bytesOut": 48213 }
```

Nunca no audit nem no log estruturado: conteúdo de log, token, kubeconfig, `Secret`,
header `Authorization`, chave privada.

---

## 11. Testes

**Unitários com fakes:** `fake.Clientset`, dynamic fake, fake discovery.

**Integração com kind**, fixtures obrigatórias:

```
deployment saudável · CrashLoopBackOff · OOMKilled · ImagePullBackOff
pod Pending por falta de recurso · liveness probe falhando
Service sem endpoints · PVC Pending · Job falhado · pod multi-container
```

Estas mesmas fixtures viram depois o corpus de `evals/` do `sre-agent`. Escrever uma
vez, usar nos dois projetos.

**Regressão de segurança (bloqueiam merge):**

```
Secret bloqueado · Secret.data nunca serializado · env.value redigido
managedFields removido · policy de namespace respeitada · policy de cluster respeitada
exec plugin bloqueado · limite de resposta respeitado · limite de log respeitado
```

**Isolamento entre clusters concorrentes** — o teste mais importante do repositório:

```
goroutine A: cluster=production, 100 chamadas
goroutine B: cluster=development, 100 chamadas
     ↓
A jamais usa credencial de development
B jamais usa credencial de production
```

Este é o bug que servidores MCP de Kubernetes com contexto global mutável têm. O teste
existe para provar que a decisão de não ter contexto global funciona sob concorrência.

---

## 12. CLI administrativa do próprio binário

```
mcp-k8s serve --transport stdio
mcp-k8s doctor
mcp-k8s config validate
mcp-k8s cluster list
mcp-k8s cluster test <name>
mcp-k8s version
```

`doctor` verifica e imprime: configuração · descoberta de clusters · conectividade com a
API · discovery · metrics disponível · permissão de list · permissão de `pods/log` ·
permissões do diretório de cache.

`doctor` é a primeira coisa que você roda quando algo não funciona, e é o que a
`srectl doctor` chama por baixo.

---

## 13. Ordem de implementação

```
01  bootstrap do repositório + Makefile + CI
02  CLI Cobra + config loader + logging estruturado
03  servidor MCP + transporte stdio + modelo de erro
04  ClusterProvider + KubeconfigProvider + InClusterProvider
05  ClientFactory (typed, dynamic, discovery, mapper)
06  discovery + RESTMapper + cache em disco
07  k8s_cluster_list · k8s_cluster_info
08  k8s_namespace_list + policy de namespace
09  k8s_resource_list + paginação + views
10  k8s_resource_get + sanitizador + policy de Secret
11  k8s_api_resources + SelfSubjectAccessReview
12  k8s_pod_logs + previous + multi-container + limites
13  k8s_events_list + normalização + coverage
14  metrics: descoberta + pods + nodes
15  resolvedor de relacionamentos (owner, Service, EndpointSlice, HPA, PDB, PVC)
16  k8s_signals
17  k8s_workload_context
18  auditoria
19  doctor
20  ambiente kind + fixtures
21  testes de regressão de segurança
22  teste de isolamento concorrente
23  Dockerfile distroless + manifests de RBAC
```

Da 07 em diante, cada tool é utilizável pela `srectl` assim que existe. Você não espera
o projeto terminar para usar.

**Pronto quando:** `srectl why deploy/<nome>` devolve contexto completo de um workload
quebrado numa fixture do kind, sem mutação, sem conteúdo de Secret, sem vazar
credencial, com limites respeitados e trilha de auditoria.

---

## 14. Roadmap

| Versão | Conteúdo |
|---|---|
| v0.2 | RancherProvider · transporte streamable HTTP · `watch` |
| v0.3 | coletor persistente de eventos (resolve o `--event-ttl`) · mais sinais |
| v0.4 | `mcp-k8s-admin` — binário separado, ServiceAccount separada, RBAC separado, com proposta, dry-run e aprovação antes de qualquer escrita |

O coletor de eventos da v0.3 não é enfeite: sem ele, correlação temporal só funciona em
incidente ao vivo — que é exatamente quando ninguém tem tempo de investigar com calma.

---

## 15. Regras permanentes do repositório

1. Nenhum nome de empresa, cliente, projeto interno ou domínio corporativo em código,
   diretório, comentário, teste, fixture ou mensagem de commit. Nem em pacote de
   provider de LLM, nem em exemplo de config. Use `example.com`, `payments`, `prod`.
2. Nada de `exec.Command("kubectl", ...)`.
3. Nenhum SDK de LLM neste repositório.
4. `internal/kubernetes` não importa nada de `internal/tools` — a dependência é
   `tools → services → kubernetes`, nunca o contrário. O código Kubernetes tem que
   funcionar sem servidor MCP.

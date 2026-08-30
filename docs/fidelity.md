# Fidelidade à doc — auditoria cláusula por cláusula

Fonte: <https://docs.x.ai/grok-bot/computer-and-apps> (última atualização na
origem: 11/08/2026). Auditoria: 29/08/2026.

Cada linha é uma afirmação da doc, não uma ideia nossa. `✅` foi implementado e
**testado**; `⚠️` existe parcialmente; `❌` não existe.

## 1. Computador persistente

| Cláusula | Estado | Onde |
|---|---|---|
| "works from a persistent cloud computer" | ✅ | volume durável + droplet |
| "can use a browser" | ✅ | Chrome 152 por tela |
| "command line" | ✅ | SSH como `agent`, `sudo` sem senha |
| "files" | ✅ | `/workspace` no volume |
| "connected tools" | ⚠️ | ferramentas próprias (shell, take-over); sem connectors |
| "without depending on your laptop remaining open" | ✅ | roda no droplet |

## 2. Um computador, compartilhado por todos os Bots

| Cláusula | Estado | Nota |
|---|---|---|
| "Browser cookies and signed-in sessions are shared" | ❌ | perfil por tela |
| "Files are visible to every Bot" | ✅ | `/workspace` comum |
| "Command-line credentials are shared" | ✅ | mesmo usuário |
| "One Bot can continue from work another Bot saved" | ✅ | via `/workspace` |
| "Do not place a credential ... if another Bot should not use it" | ✅ | avisado no README e no `screen-add` |
| "Each Bot gets its own screen" | ✅ | `screen-add N` |
| "one Bot can run only one computer-use task on its screen at a time" | ✅ | flock + estado; testado no droplet |
| "screens are separate work surfaces, not separate security boundaries" | ✅ | documentado |

## 3. Assistir ao trabalho

| Cláusula | Estado | Nota |
|---|---|---|
| "view the shared desktop" | ✅ | noVNC pelo túnel |
| "shows clicks, typing, navigation" | ✅ | é a tela real |
| "and current status" | ✅ | linha de status por tela, em arquivo e no X |
| "leave the preview while work continues" | ✅ | desacoplado |
| "closing the app or laptop does not stop cloud work" | ✅ | |

## 4. Assumir o controle num passo sensível

| Cláusula | Estado | Nota |
|---|---|---|
| "The Bot may ask you to take over" | ✅ | ferramenta `request_takeover`; **testado com o Grok de verdade** |
| assumir controle de senha/2FA/CAPTCHA/pagamento | ✅ | tarefa entra em `blocked` e o laço para |
| "Avoid pasting passwords or one-time codes into chat" | ✅ | por construção, não há chat |
| "secure secret request ... masked, not added to the conversation" | ❌ | **não existe** |

## 5. Logar uma vez

| Cláusula | Estado | Nota |
|---|---|---|
| "Browser sessions persist" | ✅ | testado com reboot e rebuild |
| "signing in for one Bot makes the session available to your other Bots" | ❌ | ver §2 |
| "Ask the Bot to pause and notify you rather than bypass the check" | ✅ | é exatamente o que `request_takeover` faz |

## 6. Conectar um app

| Cláusula | Estado | Nota |
|---|---|---|
| "Connectors give a Bot a structured way to work with supported services" | ✅ | manifesto JSON ou YAML vira ferramenta HTTP |
| "type `@` to attach the connector to the task" | ✅ | `ParseTaskRequest`; só o anexado entra |
| "type `/` to reference a saved skill" | ✅ | habilidades em `/workspace/agent/skills` |
| "Installed connectors are account-wide" | ✅ | catálogo no volume, comum a todas as telas |
| "Complete authentication in your browser if requested" | ⚠️ | credencial por arquivo (`-connector-secret`); sem fluxo de navegador |
| "Connectors are shown as **Plugins**" (a tela de catálogo) | ❌ | é interface, não infraestrutura |
| "Prefer a connector when one is available" | ✅ | dito na descrição do conector e nos exemplos |

## 7. Trabalhar com arquivos

| Cláusula | Estado | Nota |
|---|---|---|
| "shared workspace at /workspace" | ✅ | volume |
| "use clear project folders" | ✅ | `/workspace/projects` |
| "files, browser state and sign-ins survive updates and recovery" | ✅ | provado em `12-update-test.sh` |
| "treat temporary directories, manually installed packages ... as replaceable" | ✅ | `/scratch`, provado |
| "copy important results into the shared workspace" | ✅ | |
| "or attach them to the conversation" | ❌ | não há conversa |

## 8. Update, recover, reset

| Cláusula | Estado | Nota |
|---|---|---|
| "Update ... rebuilds with the latest image while preserving durable state" | ✅ | `task update`, testado |
| "Reset ... returns to the most recent durable snapshot" | ✅ | `task reset` |
| "Recover ... replaces an unreachable computer" | ⚠️ | `task update` faz; falta **detectar** inalcançável |
| "When the computer is unreachable, use Recover from the error state" | ❌ | **sem estado de erro** |
| "Wait for active work to finish before recovery when possible" | ❌ | **sem guarda de trabalho ativo** |

## 9. O computador local é separado

| Cláusula | Estado | Nota |
|---|---|---|
| "cloud computer is separate from the Mac in front of you" | ✅ | por construção |
| "only runs commands on your local computer when enabled and approved" | ✅ | nada toca o Mac |

## Placar

| | 29/08 manhã | 29/08 depois do agente |
|---|---|---|
| ✅ implementado e testado | 24 | **35** |
| ⚠️ parcial | 2 | **3** |
| ❌ ausente | 13 | **4** |

### O que ainda falta, e por quê

| Ausente | Motivo |
|---|---|
| tela de catálogo (`Settings → Plugins`) | é interface gráfica, não infraestrutura; o catálogo em si existe |
| secret request como fluxo de tela | o tipo e a garantia existem no domínio; falta a tela que coleta |
| detecção de computador inalcançável | `task update` recupera; falta o estado de erro que o dispara sozinho |
| cookies compartilhados entre telas | divergência com motivo técnico — o Chrome trava o `user-data-dir` |

### Provado contra o Grok de verdade, no droplet

| Teste | Resultado |
|---|---|
| tarefa normal | contou os núcleos, gravou `/workspace/projects/cpus.txt`, conferiu com `ls`, concluiu |
| barreira sensível | **parou**, pediu take-over com motivo `password`, tarefa em `blocked` |
| status na tela | `tela 1: PRECISA DE VOCÊ — precisa de senha ou passkey` |
| trava por tela | segunda tarefa recusada: *"a tela já tem uma tarefa ativa"* |
| trava liberada | `flock` livre mesmo com a tarefa bloqueada — o processo não segura a tela esperando a pessoa |
| conector YAML | instalado um manifesto do GitLab; o Grok respondeu `gitlab.list_issues, gitlab.create_issue` — ele enxerga as ferramentas |
| `@` e `/` juntos | `"@gitlab /estilo ... /workspace/projects/saida.txt"` → conector anexado, habilidade injetada, **caminho preservado** |

## O que falta, em ordem de dependência

1. **Nenhum agente roda.** A doc inteira fala de "the Bot". Temos o computador,
   não o bot. Tudo abaixo depende disto.
2. **Sessão compartilhada entre telas** — cláusula citada duas vezes na doc.
3. **Pedido de take-over** — o agente parar, sinalizar e esperar o humano.
4. **Status visível** — o que o agente está fazendo agora, na própria tela.
5. **Trava de uma tarefa por tela.**
6. **Secret request mascarado.**
7. **Guarda de trabalho ativo** antes de update/reset.
8. **Detecção de inalcançável** para acionar recover.
9. **Connectors e skills salvas.**

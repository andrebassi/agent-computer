#!/bin/bash
# Mostra a RESPOSTA final de cada tarefa, e nao so o estado.
#
# `agentd` imprime "tela 2: concluida" e mais nada -- a resposta vive na
# conversa gravada. Um teste que so olha o estado prova que a tarefa terminou,
# nunca que ela terminou CERTA, e foi assim que o teste da habilidade nasceu
# incapaz de reprovar.
#
# Tambem mostra as ferramentas usadas, porque na habilidade de busca a rota
# importa tanto quanto a resposta: um valor certo obtido pelo caminho errado
# (buscador em vez do atalho) e um acerto que nao se repete.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

limit="${1:-4}"

agent_ssh "bash -s" <<REMOTO
for f in \$(ls -t /workspace/agent/conversations/task-*.json 2>/dev/null | head -$limit | tac); do
  python3 - "\$f" <<'PYTHON'
import json, sys

with open(sys.argv[1]) as handle:
    conversation = json.load(handle)

messages = conversation.get("messages", [])
question = ""
toolCalls = []
answer = ""

for message in messages:
    role = message.get("Role", "")
    content = message.get("Content", "") or ""
    if role == "user" and not question:
        # A habilidade e anexada ao texto; so a pergunta interessa aqui.
        question = content.split("\n\n--- habilidade")[0].strip()
    for call in (message.get("ToolCalls") or []):
        name = call.get("Name", "?")
        args = (call.get("Arguments") or "")[:110].replace("\n", " ")
        toolCalls.append(f"{name}({args})")
    if role == "assistant" and content.strip():
        answer = content.strip()

print("=" * 72)
print("PERGUNTA:", question[:120])
print()
for i, tool in enumerate(toolCalls, 1):
    print(f"  {i}. {tool}")
print()
print("RESPOSTA:")
print(answer if answer else "  (sem resposta final)")
print()
PYTHON
done
REMOTO

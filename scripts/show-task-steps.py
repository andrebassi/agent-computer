#!/usr/bin/env python3
"""Mostra o passo a passo de uma tarefa do agente, a partir do histórico gravado.

Serve para responder "o que o agente fez, exatamente?" sem ler um JSON de
centenas de linhas. É diagnóstico e é demonstração: numa tarefa que deu errado,
a sequência de chamadas costuma mostrar onde ele se perdeu.

Uso: show-task-steps.py <caminho do arquivo de conversa>
"""
import json
import sys


def main() -> int:
    """Imprime o roteiro da tarefa: o pedido, as ferramentas e a conclusão."""
    if len(sys.argv) < 2:
        print("uso: show-task-steps.py <arquivo de conversa .json>", file=sys.stderr)
        return 1

    with open(sys.argv[1], encoding="utf-8") as handle:
        conversation = json.load(handle)

    calls = 0
    for message in conversation["messages"]:
        role = message["Role"]
        # A instrução de sistema é sempre a mesma e ocuparia a tela inteira.
        if role == "system":
            continue

        if role == "user":
            text = (message.get("Content") or "")[:90]
            print(f"  PEDIDO: {text}...\n")

        for call in message.get("ToolCalls") or []:
            calls += 1
            arguments = call["Arguments"][:65].replace("\n", " ")
            print(f"  {calls}. {call['Name']}({arguments})")

        # Resposta sem chamada de ferramenta é a conclusão da tarefa.
        if role == "assistant" and not message.get("ToolCalls") and message.get("Content"):
            print(f"\n  CONCLUSAO: {message['Content'][:180]}")

    print(f"\n  ferramentas usadas: {calls}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Renomeia identificadores em português para inglês, em arquivos Go.

Existe porque a rule de idioma exige verificação por ENUMERAÇÃO, e a correção
que ela produz é sempre a mesma: substituir nomes declarados, sem tocar em
comentário nem em literal de string — que são português por exigência da mesma
regra.

As três coisas se separam, e misturá-las já custou defeito medido:

  código               renomeia
  comentário           NUNCA — é português por exigência
  literal de string    NUNCA — é texto que alguém lê, ou valor de contrato

Passada ÚNICA com callback, e não substituições sequenciais: aplicar
`erro -> error` depois de `resumo -> summary` produz `summary.errorr`.
"""

import pathlib
import re
import sys

# Cada entrada é um identificador que vazou, com o termo em inglês que o
# substitui. A lista é o resultado da enumeração, não um chute.
RENAMES = {
    "ferramentas": "toolNames",
    "casos": "cases",
    "esperado": "expected",
    "espiao": "spy",
    "falha": "failing",
    "inicio": "start",
    "insistente": "insisting",
    "nome": "name",
    "progresso": "progress",
    "segundo": "second",
    "primeiro": "first",
    "valor": "value",
    "tarefa1": "firstTask",
    "tarefa2": "secondTask",
    "licao": "lesson",
    "lidas": "loaded",
    "tentativa": "attempt",
    "documentado": "documented",
    "descricao": "description",
    "agora": "now",
    "retomada": "resumed",
    "bloqueada": "blockedTask",
    "linhas": "lines",
    "conteudo": "content",
    "arquivo": "file",
    "nomes": "names",
    "tarefa": "task",
    "linha": "line",
    "resposta": "answer",
}

# Ordenado por tamanho decrescente para `tarefa1` casar antes de `tarefa`.
PATTERN = re.compile(r"\b(" + "|".join(sorted(RENAMES, key=len, reverse=True)) + r")\b")


def substitute(text):
    """Troca os identificadores num trecho que já se sabe ser código."""
    return PATTERN.sub(lambda match: RENAMES[match.group(1)], text)


def rewrite_line(line):
    """Devolve a linha com o código renomeado e o resto intacto."""
    stripped = line.lstrip()
    if stripped.startswith("//"):
        # Comentário inteiro: intocado. É português por exigência da regra.
        return line
    # Índices ÍMPARES do split são literais de string ou de crase.
    parts = re.split(r'("(?:[^"\\]|\\.)*"|`[^`]*`)', line)
    for i in range(0, len(parts), 2):
        marker = parts[i].find("//")
        if marker >= 0:
            # Comentário de fim de linha: só o que vem antes é código.
            parts[i] = substitute(parts[i][:marker]) + parts[i][marker:]
        else:
            parts[i] = substitute(parts[i])
    return "".join(parts)


def main(paths):
    """Reescreve cada arquivo, relatando os que mudaram."""
    for path in paths:
        handle = pathlib.Path(path)
        original = handle.read_text()
        rewritten = "\n".join(rewrite_line(line) for line in original.split("\n"))
        if rewritten != original:
            handle.write_text(rewritten)
            print(f"  renomeado: {path}")


if __name__ == "__main__":
    main(sys.argv[1:])

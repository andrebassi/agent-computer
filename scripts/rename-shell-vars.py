#!/usr/bin/env python3
"""Renomeia variáveis de shell em português para inglês.

É o par do renomeador de Go, e tem uma diferença que importa: em shell, o
conteúdo de ASPAS DUPLAS É CÓDIGO. `echo "$label"` precisa ser renomeado junto
com a declaração; pular tudo que está entre aspas quebra o script, porque ele
passa a declarar `label` e a usar `$rotulo`.

Só aspas SIMPLES são literais de verdade — e mesmo assim, o que interessa
preservar ali é texto de mensagem, não código.
"""

import pathlib
import re
import sys

# Resultado da enumeração das declarações do script, não um chute.
RENAMES = {
    "antes": "before",
    "bloqueada": "blockedTask",
    "catalogo": "catalog",
    "depois": "after",
    "estado": "state",
    "instalado": "installed",
    "linha": "line",
    "linhas": "lineCount",
    "marcador": "marker",
    "nova": "createdTask",
    "presa": "stuckTask",
    "retomada": "resumeOutput",
    "saida": "output",
    "sistema": "systemPrompt",
    "tarefa": "task",
    "turnos": "turns",
}

# `\b` não basta em shell: `$saida` e `${saida}` precisam casar, e `saidaX` não.
PATTERN = re.compile(
    r"(?<![A-Za-z0-9_])(" + "|".join(sorted(RENAMES, key=len, reverse=True)) + r")(?![A-Za-z0-9_])"
)


def substitute(text):
    """Troca os nomes num trecho que já se sabe ser código."""
    return PATTERN.sub(lambda match: RENAMES[match.group(1)], text)


def rewrite_line(line):
    """Devolve a linha com o código renomeado e o comentário intacto."""
    stripped = line.lstrip()
    if stripped.startswith("#"):
        # Comentário inteiro: intocado. É português por exigência da regra.
        return line
    # Índices ÍMPARES são literais de aspas SIMPLES. Aspas duplas ficam de fora
    # da separação de propósito: ali dentro há expansão de variável, que é código.
    parts = re.split(r"('[^']*')", line)
    for i in range(0, len(parts), 2):
        marker = parts[i].find("#")
        if marker >= 0:
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

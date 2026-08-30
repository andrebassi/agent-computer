# Revisão de mudança antes de publicar

Procedimento para conferir o próprio trabalho antes de dar por pronto.

## 1. Leia o diff inteiro, não o resumo

```
git diff --stat
git diff
```

O resumo esconde o que importa. Um arquivo com "+3 −1" pode ser a linha que
inverte uma condição.

## 2. Confira que o commit leva o que a mensagem promete

```
git diff --cached --name-only
```

Compare com o que você escreveu na mensagem. Arquivo prometido e ausente é o
defeito mais fácil de cometer e o mais difícil de notar depois — em especial
quando algum padrão de ignore o exclui em silêncio.

## 3. Rode o que verifica, e leia o código de saída

Não confie na saída na tela: comando em pipe devolve o código do último
processo, não do que interessa.

```
<comando de teste>; echo "rc=$?"
```

Se o projeto tem gate de cobertura ou de lint, rode o gate — não o relatório.
Relatório imprime o número e sai com sucesso mesmo abaixo do piso.

## 4. Procure segredo antes de publicar

```
git diff --cached | grep -inE 'senha|password|token|secret|api[_-]?key|BEGIN [A-Z ]*PRIVATE KEY'
```

Varra também por NOME de arquivo, não só por conteúdo: `.env`, `.pem`, `.key`.

## 5. Pergunte-se o que quebraria

Antes de publicar, responda em uma frase: **se esta mudança estiver errada, como
alguém descobre?** Se a resposta for "só quando der problema em produção",
falta um teste ou um alerta.

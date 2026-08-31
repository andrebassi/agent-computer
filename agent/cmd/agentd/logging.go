package main

import (
	"log/slog"
	"os"
	"strings"
)

// newLogger monta o registrador do serviço, em JSON e com nível ajustável.
//
// Duas mudanças em relação ao que havia, e as duas têm consequência prática:
//
//  1. JSON em vez de texto. O handler de texto do `slog` produz linha que só
//     pessoa lê; para um ingestor extrair campo dela é preciso regex, e regex
//     sobre log é a coisa que quebra em silêncio quando alguém acrescenta um
//     campo. Em JSON o campo tem nome, e continua tendo depois da mudança.
//
//  2. Nível vindo do ambiente. Antes o handler subia com `opts` nil, o que fixa
//     o nível em Info — e o código não tinha um único `Debug`, porque não
//     adiantaria escrevê-lo. Diagnóstico exigia recompilar, e teto que só muda
//     recompilando é teto desligado (é a mesma lição que os guardrails já
//     registram sobre os limiares deles).
//
// Vai para stderr, que a unidade systemd entrega ao journald. O destino não
// muda: o que muda é o formato poder ser consumido por máquina.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel(),
	}))
}

// logLevel lê o nível de AGENTD_LOG_LEVEL, ou devolve Info.
//
// Valor irreconhecível cai no padrão em SILÊNCIO, de propósito, seguindo o que
// os limiares dos guardrails já fazem: uma variável malformada não pode
// impedir o serviço de subir. Um agente que não sobe porque alguém digitou
// "DEBUGG" seria pior que um agente pouco falante.
func logLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTD_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

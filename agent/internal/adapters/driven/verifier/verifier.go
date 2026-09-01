// Package verifier implementa a verificação de conclusão perguntando ao modelo.
//
// # Por que uma pergunta separada, e não um campo na resposta da tarefa
//
// Pedir ao modelo, no mesmo turno, que faça o trabalho E declare se o cumpriu
// junta duas coisas que se contaminam: quem acabou de dizer "pronto" tende a
// confirmar. A verificação vem depois, numa conversa curta que recebe o pedido
// original e o histórico, e cuja única saída possível é o veredicto.
//
// # Por que ferramenta, e não JSON no texto
//
// Pedir JSON em texto livre exige parsear o que voltar, e o que volta às vezes
// tem cerca de markdown, prosa antes do objeto, ou uma vírgula a mais. Declarar
// uma ferramenta com schema empurra a estruturação para o fornecedor, que já
// valida os argumentos — e o modo de falha vira "não chamou a ferramenta", que
// é detectável, em vez de "chamou e o parser errou", que é silencioso.
package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// reportVerdictTool é a única saída que a conversa de verificação aceita.
const reportVerdictTool = "report_verdict"

// verdictSchema descreve os três campos do veredicto.
//
// `evidence` é obrigatório junto com `met`, e isso é deliberado: um verificador
// que pudesse aprovar sem apontar onde aprovaria por inércia. Exigir a citação
// torna a aprovação falsa mais cara que a honesta.
const verdictSchema = `{
  "type": "object",
  "properties": {
    "met": {
      "type": "boolean",
      "description": "true somente se TUDO o que foi pedido foi feito"
    },
    "evidence": {
      "type": "string",
      "description": "o trecho do histórico que comprova o veredicto, citado"
    },
    "missing": {
      "type": "string",
      "description": "quando met=false, o que falta fazer, redigido como tarefa"
    }
  },
  "required": ["met", "evidence"]
}`

// systemPrompt instrui o verificador.
//
// Três exigências, cada uma contra um modo de falha observado em verificação
// por modelo: aprovar por gentileza, inventar evidência, e transformar
// verificação em nova tarefa ("eu faria diferente" não é "não foi feito").
const systemPrompt = `Você verifica se uma tarefa foi cumprida. Não a executa.

Receberá o PEDIDO original e o HISTÓRICO do que o agente fez. Responda SEMPRE
chamando a ferramenta report_verdict.

Regras:
1. met=true só se TUDO o que foi pedido foi feito. Cumprido em parte é met=false.
2. evidence cita o que está no histórico. Se não encontrar nada que comprove,
   isso é met=false — ausência de evidência não é evidência.
3. Julgue o que foi PEDIDO, não como você faria. Abordagem diferente da sua,
   com o pedido atendido, é met=true.
4. Em missing, escreva o que falta como tarefa a fazer, não como crítica.`

// Verifier pergunta ao modelo se a tarefa foi cumprida.
type Verifier struct {
	model ports.LanguageModel
}

// New monta o verificador sobre um modelo já configurado.
//
// Recebe o mesmo porto que o laço usa, e não um cliente concreto: quem decide
// qual modelo verifica é o ponto de composição, e nada aqui precisa saber se é
// o mesmo que executou a tarefa.
func New(model ports.LanguageModel) *Verifier {
	return &Verifier{model: model}
}

// Verify monta a conversa de verificação e traduz a resposta em veredicto.
//
// Erro de rede ou modelo que não chama a ferramenta viram ERRO, e não veredicto
// negativo. A diferença é o que impede uma indisponibilidade de bloquear tarefa
// boa: quem chama trata erro como abstenção.
func (v *Verifier) Verify(
	ctx context.Context,
	request string,
	history []domain.Message,
) (service.Verdict, error) {
	messages := []domain.Message{
		{Role: domain.RoleSystem, Content: systemPrompt},
		{Role: domain.RoleUser, Content: buildQuestion(request, history)},
	}
	specs := []ports.ToolSpec{{
		Name:        reportVerdictTool,
		Description: "Registra o veredicto sobre a tarefa. Chame exatamente uma vez.",
		Schema:      verdictSchema,
	}}

	completion, err := v.model.Complete(ctx, messages, specs)
	if err != nil {
		return service.Verdict{}, fmt.Errorf("verificação falhou: %w", err)
	}
	for _, call := range completion.ToolCalls {
		if call.Name == reportVerdictTool {
			return decodeVerdict(call.Arguments)
		}
	}
	// Não chamar a ferramenta é ERRO, e não reprovação: significa que a
	// verificação não aconteceu, o que é diferente de ter acontecido e dado
	// negativo. Tratar como negativo bloquearia a tarefa por um defeito do
	// verificador.
	return service.Verdict{}, fmt.Errorf(
		"o verificador não chamou %s (parou por %q)", reportVerdictTool, completion.StopReason)
}

// buildQuestion monta o texto que o verificador lê.
//
// O histórico entra RESUMIDO por papel e truncado, não inteiro: a conversa de
// uma tarefa longa passa de 80 mensagens, e reenviá-la dobraria o custo de
// token da tarefa só para verificar. O que importa para o veredicto é o que foi
// feito e o que voltou — não cada byte de saída de comando.
func buildQuestion(request string, history []domain.Message) string {
	var builder strings.Builder
	builder.WriteString("PEDIDO ORIGINAL:\n")
	builder.WriteString(request)
	builder.WriteString("\n\nHISTÓRICO DO QUE O AGENTE FEZ:\n")

	for _, message := range history {
		if message.Role == domain.RoleSystem {
			// A instrução do sistema não é trabalho feito, e ocupa espaço que a
			// evidência precisa.
			continue
		}
		builder.WriteString("[")
		builder.WriteString(string(message.Role))
		builder.WriteString("] ")
		builder.WriteString(truncate(message.Content, maxExcerpt))
		builder.WriteString("\n")
	}
	return builder.String()
}

// maxExcerpt limita cada mensagem no texto enviado ao verificador.
//
// 600 caracteres cobrem a saída típica de um comando e o texto de uma decisão.
// Acima disso o que aparece é conteúdo de página e despejo de log, que aumentam
// o custo sem melhorar o veredicto.
const maxExcerpt = 600

// truncate corta a mensagem e marca o corte.
//
// A marca importa: sem ela o verificador leria um comando pela metade como se
// fosse o comando inteiro, e poderia concluir que algo não foi feito porque a
// prova ficou depois do corte.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + fmt.Sprintf("… [+%d caracteres]", len(runes)-limit)
}

// decodeVerdict traduz os argumentos da ferramenta em veredicto.
func decodeVerdict(arguments string) (service.Verdict, error) {
	var payload struct {
		Met      bool   `json:"met"`
		Evidence string `json:"evidence"`
		Missing  string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return service.Verdict{}, fmt.Errorf("veredicto ilegível: %w", err)
	}
	// Recusa sem dizer o que falta é inútil para quem recebe: a devolução ao
	// modelo ficaria "falta: " e o bloqueio não diria à pessoa o que fazer.
	if !payload.Met && strings.TrimSpace(payload.Missing) == "" {
		payload.Missing = "o pedido não foi cumprido, e o verificador não detalhou o que falta"
	}
	return service.Verdict{
		Met:      payload.Met,
		Evidence: payload.Evidence,
		Missing:  payload.Missing,
	}, nil
}

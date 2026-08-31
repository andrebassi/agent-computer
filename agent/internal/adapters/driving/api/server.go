package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// maxBodyBytes limita o corpo de uma requisição.
//
// O texto de uma tarefa cabe folgado em 64 KB. Sem teto, uma requisição enorme
// consome memória do processo que está segurando telas — e derrubar o servidor
// não exigiria nem autenticação, porque o corpo é lido antes de qualquer coisa.
const maxBodyBytes = 64 << 10

// Server expõe as tarefas por HTTP.
type Server struct {
	sup   *Supervisor
	life  *service.Lifecycle
	store ports.TaskStore
	token string
	log   *slog.Logger
}

// NewServer monta o servidor. Token vazio é erro de programação, e falhar aqui
// é melhor do que subir uma porta aberta.
func NewServer(sup *Supervisor, life *service.Lifecycle, store ports.TaskStore,
	token string, log *slog.Logger) (*Server, error) {
	if token == "" {
		return nil, errors.New("token vazio: a porta não sobe sem autenticação")
	}
	return &Server{sup: sup, life: life, store: store, token: token, log: log}, nil
}

// Handler monta as rotas.
//
// `net/http` do Go 1.22+ roteia por método e parâmetro de caminho, então não
// entra dependência de roteador — a superfície de terceiros deste projeto é um
// ativo, e são três dependências diretas ao todo.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Saúde fica FORA da autenticação e não revela nada além de estar no ar.
	// Um health autenticado obrigaria o supervisor de processo a carregar o
	// segredo só para provar que a porta responde.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.Handle("POST /tasks", s.authenticated(s.createTask))
	mux.Handle("GET /tasks/{id}", s.authenticated(s.getTask))
	mux.Handle("POST /tasks/{id}/resume", s.authenticated(s.resumeTask))
	mux.Handle("POST /tasks/{id}/abandon", s.authenticated(s.abandonTask))

	return mux
}

// authenticated exige o token no cabeçalho.
func (s *Server) authenticated(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Comparação de tempo constante. `==` em Go sai no primeiro byte
		// diferente, e a diferença de tempo entre "errou no byte 1" e "errou no
		// byte 30" é medível pela rede — é assim que se descobre um token byte
		// a byte.
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "token ausente ou inválido", nil)
			return
		}
		next(w, r)
	})
}

// createRequest é o corpo de uma tarefa nova.
type createRequest struct {
	Prompt string `json:"prompt"`
	Screen int    `json:"screen"`
}

// noteRequest é o corpo da retomada.
type noteRequest struct {
	Note string `json:"note"`
}

// taskResponse é como uma tarefa aparece no fio.
//
// Os nomes são em snake_case porque é o que quem consome HTTP espera; são valor
// de contrato, e renomeá-los quebra o cliente. contract:ok
type taskResponse struct {
	ID          string    `json:"id"`
	State       string    `json:"state"`
	Screen      int       `json:"screen"`
	BlockReason string    `json:"block_reason,omitempty"`
	BlockDetail string    `json:"block_detail,omitempty"`
	Answer      string    `json:"answer,omitempty"`
	Failure     string    `json:"failure,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// createTask inicia uma tarefa nova.
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Screen == 0 {
		// Tela 1 é o padrão do comando de linha; manter o mesmo aqui evita que
		// omitir o campo signifique coisas diferentes nas duas pontas.
		req.Screen = 1
	}

	task, err := s.sup.Start(r.Context(), req.Screen, req.Prompt)
	if err != nil {
		s.writeStartError(w, err)
		return
	}
	w.Header().Set("Location", "/tasks/"+task.ID)
	writeJSON(w, http.StatusCreated, s.describe(r, task))
}

// writeStartError traduz a falha de criação no código HTTP certo.
func (s *Server) writeStartError(w http.ResponseWriter, err error) {
	var busy *BusyError
	switch {
	case errors.As(err, &busy):
		// O corpo diz o que fazer: sem isso, quem chamou precisa adivinhar se
		// retoma ou abandona.
		writeError(w, http.StatusConflict, busy.Error(), map[string]string{
			"task_id": busy.Task.ID,
			"state":   string(busy.Task.State),
			"hint": fmt.Sprintf("POST /tasks/%s/resume ou POST /tasks/%s/abandon",
				busy.Task.ID, busy.Task.ID),
		})
	case errors.Is(err, ErrTooManyTasks):
		// 429, e não 409: o problema não é ESTA tela, é a máquina cheia.
		// Mandar 409 faria o cliente tentar abandonar uma tarefa que não é a
		// causa, ou trocar de tela — e a próxima falharia igual.
		writeError(w, http.StatusTooManyRequests, err.Error(), map[string]string{
			"hint": "espere uma tarefa terminar, ou consulte GET /tasks/<id> das que estão rodando",
		})
	case errors.Is(err, domain.ErrScreenBusy):
		writeError(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, err.Error(), nil)
	case errors.Is(err, domain.ErrInvalidTask):
		writeError(w, http.StatusBadRequest, err.Error(), nil)
	default:
		s.log.Error("falha ao criar tarefa", "erro", err)
		writeError(w, http.StatusInternalServerError, "não foi possível criar a tarefa", nil)
	}
}

// getTask devolve o estado atual de uma tarefa.
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.loadOr404(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.describe(r, task))
}

// resumeTask devolve o controle ao agente depois do take-over.
func (s *Server) resumeTask(w http.ResponseWriter, r *http.Request) {
	var req noteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	task, err := s.sup.Resume(r.Context(), r.PathValue("id"), req.Note)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTaskNotFound):
			writeError(w, http.StatusNotFound, err.Error(), nil)
		case errors.Is(err, service.ErrNotBlocked), errors.Is(err, domain.ErrScreenBusy):
			writeError(w, http.StatusConflict, err.Error(), nil)
		case errors.Is(err, ErrShuttingDown):
			writeError(w, http.StatusServiceUnavailable, err.Error(), nil)
		default:
			s.log.Error("falha ao retomar", "erro", err)
			writeError(w, http.StatusInternalServerError, "não foi possível retomar", nil)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, s.describe(r, task))
}

// abandonTask desiste da tarefa e libera a tela.
func (s *Server) abandonTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Cancelar ANTES de marcar no disco: se a tarefa é deste processo, o
	// abandono interrompe de verdade em vez de só anotar que foi abandonada.
	s.sup.Cancel(id)

	task, err := s.life.Abandon(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTaskNotFound):
			writeError(w, http.StatusNotFound, err.Error(), nil)
		case errors.Is(err, service.ErrTaskFinished):
			writeError(w, http.StatusConflict, err.Error(), nil)
		default:
			s.log.Error("falha ao abandonar", "erro", err)
			writeError(w, http.StatusInternalServerError, "não foi possível abandonar", nil)
		}
		return
	}
	writeJSON(w, http.StatusOK, s.describe(r, task))
}

// loadOr404 carrega a tarefa ou responde 404.
func (s *Server) loadOr404(w http.ResponseWriter, r *http.Request) (*domain.Task, bool) {
	task, err := s.store.LoadTask(r.Context(), r.PathValue("id"))
	if err != nil {
		s.log.Error("falha ao carregar tarefa", "erro", err)
		writeError(w, http.StatusInternalServerError, "não foi possível ler a tarefa", nil)
		return nil, false
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "tarefa não encontrada", nil)
		return nil, false
	}
	return task, true
}

// describe converte a tarefa para o formato do fio, incluindo a resposta.
//
// A resposta vem da conversa, e não da tarefa: "a resposta é a última fala do
// assistente com conteúdo" é regra de produto que mora no domínio, e lê-la de
// outro lugar faria as pontas divergirem sobre o que a tarefa respondeu.
func (s *Server) describe(r *http.Request, task *domain.Task) taskResponse {
	out := taskResponse{
		ID: task.ID, State: string(task.State), Screen: task.Screen,
		BlockReason: string(task.BlockReason), BlockDetail: task.BlockDetail,
		Failure: task.Failure, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
	if conv, err := s.store.LoadConversation(r.Context(), task.ID); err == nil && conv != nil {
		out.Answer = conv.LastAnswer()
	}
	return out
}

// decodeBody lê o corpo JSON, respondendo o erro certo quando não dá.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	// Campo desconhecido RECUSA, em vez de ser ignorado em silencio.
	//
	// O padrao do Go e ignorar, e isso esconde erro de cliente: quem escrever
	// `"screens": 3` em vez de `"screen": 3` recebe 201 e a tarefa vai para a
	// tela 1, sem nada indicando o engano. Num cliente que dispara trabalho numa
	// maquina compartilhada, isso e a diferenca entre "errei o campo" e "por que
	// esta rodando na tela errada?".
	//
	// A API tem um cliente so -- os scripts deste repositorio --, entao a
	// rigidez nao custa compatibilidade. Achado pelo teste hostil em 30/08/2026,
	// que mandou {"prompt":"oi","screen":2,"admin":true} e recebeu 201.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "corpo grande demais", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, "corpo inválido: "+err.Error(), nil)
		return false
	}
	return true
}

// writeJSON responde com o objeto serializado.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError responde um erro, com campos extras quando eles ajudam a agir.
func writeError(w http.ResponseWriter, status int, message string, extra map[string]string) {
	body := map[string]any{"error": message}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

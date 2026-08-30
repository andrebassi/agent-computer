package ports

import "errors"

// ErrContextTooLong diz que o histórico não cabe mais na janela do modelo.
//
// É erro de PORTO, e não do fornecedor, porque quem sabe reagir é o serviço: a
// correção é encurtar a conversa, e o que pode ser descartado é regra de
// produto — instrução de sistema fica, resultado de ferramenta não pode ficar
// órfão da chamada que o gerou.
//
// O adaptador só reconhece a evidência (o código HTTP e o corpo) e a traduz para
// cá. Se o serviço tivesse de farejar a mensagem de erro para descobrir isso,
// estaria codificando o vocabulário de um fornecedor específico — que é
// exatamente o acoplamento que os portos existem para impedir.
var ErrContextTooLong = errors.New("janela de contexto estourada")

// ErrModelUnavailable cobre falha transitória — rede, tempo esgotado, 429, 5xx —
// que o adaptador JÁ tentou repetir.
//
// Chega aqui só depois de a repetição esgotar. O serviço nunca repete por conta
// própria: repetir é detalhe de transporte, e duplicar essa lógica nas duas
// camadas produziria tentativas multiplicadas sem ninguém perceber.
var ErrModelUnavailable = errors.New("modelo indisponível")

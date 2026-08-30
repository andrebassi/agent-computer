package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/connectors"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/secret"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/skills"
	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// runCatalog atende os comandos de catálogo: listar, instalar e remover
// conectores e habilidades, e gravar credenciais.
//
// É o equivalente em linha de comando à tela de catálogo que a documentação
// descreve. Uma interface gráfica faria o mesmo com mais trabalho e menos
// utilidade aqui: o computador é operado por SSH, e uma tela a mais seria uma
// tela a mais para manter.
func runCatalog(stateDir string, args []string) error {
	if len(args) == 0 {
		printCatalogHelp()
		return nil
	}

	registry, err := connectors.NewRegistry(stateDir + "/connectors")
	if err != nil {
		return err
	}
	skillStore, err := skills.NewStore(stateDir + "/skills")
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return listCatalog(registry, skillStore)
	case "install":
		if len(args) < 2 {
			return fmt.Errorf("uso: agentd -catalog install <caminho do manifesto>")
		}
		if err := registry.InstallFile(args[1]); err != nil {
			return err
		}
		fmt.Printf("conector instalado a partir de %s\n", args[1])
		return warnIfMissingSecret(registry, args[1])
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("uso: agentd -catalog remove <nome do conector>")
		}
		if err := registry.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("conector %q removido (a credencial NÃO foi apagada)\n", args[1])
		return nil
	case "secret":
		if len(args) < 2 {
			return fmt.Errorf("uso: agentd -catalog secret <referência>")
		}
		return setSecret(registry, args[1])
	case "skill-save":
		if len(args) < 3 {
			return fmt.Errorf("uso: agentd -catalog skill-save <nome> <arquivo>")
		}
		content, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		if err := skillStore.Save(args[1], string(content)); err != nil {
			return err
		}
		fmt.Printf("habilidade %q salva (%d bytes) — use com /%s\n", args[1], len(content), args[1])
		return nil
	case "skill-remove":
		if len(args) < 2 {
			return fmt.Errorf("uso: agentd -catalog skill-remove <nome>")
		}
		if err := skillStore.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("habilidade %q removida\n", args[1])
		return nil
	}
	printCatalogHelp()
	return fmt.Errorf("comando desconhecido: %q", args[0])
}

// listCatalog mostra o que está instalado, com a situação da credencial.
//
// A situação da credencial é a informação que evita o modo de falha mais comum:
// conector instalado, credencial esquecida, e o agente falhando na primeira
// chamada com um erro que parece vir da API.
func listCatalog(registry *connectors.Registry, skillStore *skills.Store) error {
	installed := registry.Installed()
	fmt.Printf("CONECTORES (%d)\n", len(installed))
	if len(installed) == 0 {
		fmt.Println("  nenhum — instale com: agentd -catalog install <manifesto>")
	}
	for _, c := range installed {
		auth := "sem autenticação"
		if c.RequiresAuth() {
			if registry.HasSecret(c.SecretRef) {
				auth = "credencial configurada"
			} else {
				auth = "⚠️  CREDENCIAL FALTANDO — agentd -catalog secret " + c.SecretRef
			}
		}
		fmt.Printf("\n  @%s — %s\n", c.Name, auth)
		if c.Description != "" {
			fmt.Printf("     %s\n", firstLine(c.Description))
		}
		for _, name := range domain.SortedToolNames([]*domain.Connector{c}) {
			fmt.Printf("     · %s\n", name)
		}
	}

	names, err := skillStore.List()
	if err != nil {
		return err
	}
	fmt.Printf("\nHABILIDADES (%d)\n", len(names))
	if len(names) == 0 {
		fmt.Println("  nenhuma — salve com: agentd -catalog skill-save <nome> <arquivo.md>")
	}
	for _, name := range names {
		content, err := skillStore.Get(name)
		if err != nil {
			continue
		}
		fmt.Printf("  /%s — %s\n", name, firstLine(content))
	}
	return nil
}

// setSecret pede a credencial no terminal, com o eco desligado, e a grava.
func setSecret(registry *connectors.Registry, ref string) error {
	req, err := domain.NewSecretRequest(ref, "credencial do conector "+ref, "arquivo local em connectors/secrets")
	if err != nil {
		return err
	}
	value, err := secret.NewTerminalPrompter().Prompt(context.Background(), 0, req)
	if err != nil {
		return err
	}
	if err := registry.SetSecret(ref, value); err != nil {
		return err
	}
	// O valor não é ecoado nem no sucesso: só o comprimento, que basta para a
	// pessoa perceber se colou algo truncado.
	fmt.Printf("credencial %q gravada (%d caracteres, permissão 0600)\n", ref, len(value))
	return nil
}

// warnIfMissingSecret avisa, logo após instalar, que falta a credencial.
//
// Avisar aqui e não só na primeira falha economiza um ciclo inteiro de tarefa
// gasto para descobrir algo que já se sabia no momento da instalação.
func warnIfMissingSecret(registry *connectors.Registry, path string) error {
	for _, c := range registry.Installed() {
		if !strings.Contains(path, c.Name) {
			continue
		}
		if c.RequiresAuth() && !registry.HasSecret(c.SecretRef) {
			fmt.Printf("\n⚠️  falta a credencial. Configure com:\n")
			fmt.Printf("   agentd -catalog secret %s\n", c.SecretRef)
		}
	}
	return nil
}

// firstLine devolve a primeira linha não vazia, encurtada, para a listagem
// caber na tela.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		if len(line) > 70 {
			return line[:70] + "..."
		}
		return line
	}
	return ""
}

// printCatalogHelp explica os comandos de catálogo.
func printCatalogHelp() {
	fmt.Print(`uso: agentd -catalog <comando>

  list                          lista conectores e habilidades, com a credencial
  install <manifesto>           instala conector de um .json, .yaml ou .yml
  remove <nome>                 remove conector (a credencial fica)
  secret <referência>           grava credencial, pedindo o valor sem eco
  skill-save <nome> <arquivo>   salva habilidade a partir de um arquivo
  skill-remove <nome>           remove habilidade

depois de instalar, use na tarefa:
  agentd -prompt "@<conector> ... /<habilidade> ..."
`)
}

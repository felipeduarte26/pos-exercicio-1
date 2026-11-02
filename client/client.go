// Client da cotação do dólar.
//
// Pede a cotação ao server (GET http://localhost:8080/cotacao) com limite de
// 300ms e grava o valor recebido em cotacao.txt no formato "Dólar: {valor}".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	urlServidor    = "http://localhost:8080/cotacao"
	timeoutCliente = 300 * time.Millisecond
	arquivoSaida   = "cotacao.txt"
)

// respostaCotacao é o JSON devolvido pelo server.
type respostaCotacao struct {
	Bid string `json:"bid"`
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Fatalf("[timeout] o servidor não respondeu em %s: %v", timeoutCliente, err)
		}
		log.Fatalf("[erro] %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutCliente)
	defer cancel()

	bid, err := buscarCotacao(ctx)
	if err != nil {
		return err
	}

	conteudo := fmt.Sprintf("Dólar: %s\n", bid)
	if err := os.WriteFile(arquivoSaida, []byte(conteudo), 0o644); err != nil {
		return fmt.Errorf("gravar %s: %w", arquivoSaida, err)
	}

	log.Printf("cotação gravada em %s -> Dólar: %s", arquivoSaida, bid)
	return nil
}

// buscarCotacao chama o server e devolve apenas o campo bid. O mesmo contexto
// cobre a requisição e a leitura do corpo, então os 300ms valem para a
// resposta inteira.
func buscarCotacao(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlServidor, nil)
	if err != nil {
		return "", fmt.Errorf("montar requisição: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("chamar servidor: %w", err)
	}
	defer resp.Body.Close()

	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ler resposta do servidor: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("servidor respondeu %s: %s", resp.Status, bytes.TrimSpace(corpo))
	}

	var cotacao respostaCotacao
	if err := json.Unmarshal(corpo, &cotacao); err != nil {
		return "", fmt.Errorf("decodificar resposta do servidor: %w", err)
	}
	if cotacao.Bid == "" {
		return "", errors.New("resposta do servidor sem o campo bid")
	}
	return cotacao.Bid, nil
}

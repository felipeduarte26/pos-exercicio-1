// Server da cotação do dólar.
//
// Expõe GET /cotacao na porta 8080. A cada requisição, consulta a cotação
// USD-BRL na AwesomeAPI (timeout de 200ms), registra a cotação no SQLite
// (timeout de 10ms) e devolve ao client o valor atual do câmbio (bid) em JSON.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	enderecoServidor = ":8080"
	urlAPI           = "https://economia.awesomeapi.com.br/json/last/USD-BRL"

	timeoutAPI   = 200 * time.Millisecond
	timeoutBanco = 10 * time.Millisecond

	// WAL com synchronous=NORMAL dispensa um fsync a cada INSERT, o que mantém
	// a gravação bem abaixo dos 10ms mesmo em discos mais lentos.
	dsnBanco = "file:cotacoes.db?_journal_mode=WAL&_synchronous=NORMAL"
)

// Cotacao é o objeto "USDBRL" devolvido pela AwesomeAPI.
type Cotacao struct {
	Code       string `json:"code"`
	Codein     string `json:"codein"`
	Name       string `json:"name"`
	High       string `json:"high"`
	Low        string `json:"low"`
	VarBid     string `json:"varBid"`
	PctChange  string `json:"pctChange"`
	Bid        string `json:"bid"`
	Ask        string `json:"ask"`
	Timestamp  string `json:"timestamp"`
	CreateDate string `json:"create_date"`
}

type respostaAPI struct {
	USDBRL Cotacao `json:"USDBRL"`
}

// respostaCotacao é o JSON enviado ao client: somente o valor atual do câmbio.
type respostaCotacao struct {
	Bid string `json:"bid"`
}

type respostaErro struct {
	Erro string `json:"erro"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := abrirBanco(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /cotacao", handlerCotacao(db))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", enderecoServidor)
	if err != nil {
		return fmt.Errorf("abrir a porta %s: %w", enderecoServidor, err)
	}
	log.Printf("servidor ouvindo em http://localhost%s/cotacao", enderecoServidor)

	erroServidor := make(chan error, 1)
	go func() { erroServidor <- srv.Serve(ln) }()

	select {
	case err := <-erroServidor:
		return fmt.Errorf("servidor HTTP: %w", err)
	case <-ctx.Done():
		log.Println("encerrando o servidor...")
	}

	ctxShutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctxShutdown)
}

// abrirBanco abre (ou cria) o arquivo cotacoes.db e garante que a tabela exista.
func abrirBanco(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsnBanco)
	if err != nil {
		return nil, fmt.Errorf("abrir banco: %w", err)
	}

	// O SQLite aceita um escritor por vez. Com uma única conexão, requisições
	// simultâneas esperam a vez no pool (respeitando o contexto) em vez de
	// falhar com "database is locked".
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS cotacoes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			code        TEXT NOT NULL,
			codein      TEXT NOT NULL,
			name        TEXT,
			high        TEXT,
			low         TEXT,
			var_bid     TEXT,
			pct_change  TEXT,
			bid         TEXT NOT NULL,
			ask         TEXT,
			timestamp   TEXT,
			create_date TEXT,
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("criar tabela cotacoes: %w", err)
	}
	return db, nil
}

// handlerCotacao atende GET /cotacao.
func handlerCotacao(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cotacao, err := buscarCotacao(r.Context())
		if err != nil {
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				log.Printf("[timeout] a API de câmbio não respondeu em %s: %v", timeoutAPI, err)
				responderJSON(w, http.StatusGatewayTimeout, respostaErro{Erro: "a API de câmbio excedeu o tempo limite"})
			case errors.Is(err, context.Canceled):
				log.Printf("[cancelado] o client encerrou a requisição antes da resposta da API: %v", err)
			default:
				log.Printf("[erro] falha ao consultar a API de câmbio: %v", err)
				responderJSON(w, http.StatusBadGateway, respostaErro{Erro: "falha ao consultar a API de câmbio"})
			}
			return
		}

		// A cotação já foi obtida, então ela é registrada mesmo que o client
		// desista no meio do caminho (WithoutCancel). O limite de 10ms continua
		// valendo dentro de salvarCotacao.
		err = salvarCotacao(context.WithoutCancel(r.Context()), db, cotacao)
		switch {
		case err == nil:
			log.Printf("cotação registrada: bid=%s", cotacao.Bid)
		case errors.Is(err, context.DeadlineExceeded):
			log.Printf("[timeout] a gravação no banco excedeu %s: %v", timeoutBanco, err)
		default:
			log.Printf("[erro] falha ao gravar a cotação no banco: %v", err)
		}

		// Uma falha na gravação fica registrada no log, mas não impede o client
		// de receber a cotação.
		responderJSON(w, http.StatusOK, respostaCotacao{Bid: cotacao.Bid})
	}
}

// buscarCotacao consulta a AwesomeAPI com limite de 200ms. O contexto deriva da
// requisição do client: se ele desistir, a chamada externa também é cancelada.
func buscarCotacao(ctx context.Context) (Cotacao, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutAPI)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlAPI, nil)
	if err != nil {
		return Cotacao{}, fmt.Errorf("montar requisição: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Cotacao{}, fmt.Errorf("chamar API: %w", err)
	}
	defer resp.Body.Close()

	// O corpo é lido dentro do mesmo contexto, então os 200ms valem para a
	// resposta inteira, e não só para os cabeçalhos.
	corpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return Cotacao{}, fmt.Errorf("ler resposta da API: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Cotacao{}, fmt.Errorf("API respondeu %s: %s", resp.Status, bytes.TrimSpace(corpo))
	}

	var dados respostaAPI
	if err := json.Unmarshal(corpo, &dados); err != nil {
		return Cotacao{}, fmt.Errorf("decodificar resposta da API: %w", err)
	}
	if dados.USDBRL.Bid == "" {
		return Cotacao{}, errors.New("resposta da API sem o campo bid")
	}
	return dados.USDBRL, nil
}

// salvarCotacao grava a cotação no SQLite com limite de 10ms.
func salvarCotacao(ctx context.Context, db *sql.DB, c Cotacao) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutBanco)
	defer cancel()

	_, err := db.ExecContext(ctx, `
		INSERT INTO cotacoes
			(code, codein, name, high, low, var_bid, pct_change, bid, ask, timestamp, create_date)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Code, c.Codein, c.Name, c.High, c.Low, c.VarBid, c.PctChange,
		c.Bid, c.Ask, c.Timestamp, c.CreateDate,
	)
	if err != nil {
		return fmt.Errorf("inserir cotação: %w", err)
	}
	return nil
}

func responderJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[erro] falha ao escrever a resposta: %v", err)
	}
}

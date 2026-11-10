package discovery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Checkout real do app-java-angular no SHA das rodadas 7 a 10 (nomes neutralizados).
const appJavaCheckout = "../signals/code/testdata/app-java-angular-ede6c91"

const (
	svcModule = "backend/src/main/java/com/example/app/service"
	svcFile   = svcModule + "/UrlShortenerService.java"
)

func TestDeclName(t *testing.T) {
	cases := []struct{ line, want string }{
		{"func (r *Runner) Run(ctx context.Context) error {", "Run"},
		{"func overlaps(f signals.Finding) map[string]judge.Overlap {", "overlaps"},
		{"func Map[T any](xs []T) []T {", "Map"},
		{"    public boolean deleteUrl(String shortCode) {", "deleteUrl"},
		{"    private static String build(String a,", "build"},
		{"    public UrlShortenerService(", "UrlShortenerService"},
		{"    void reset() {", "reset"},
		{"    def handle_event(self, e):", "handle_event"},
		{"export async function loadUrls(id: string) {", "loadUrls"},
		{"export const shorten = async (url: string) => {", "shorten"},
		{"  ngOnInit(): void {", "ngOnInit"},
		{"    override fun onCreate(state: Bundle?) {", "onCreate"},
		// Fase C, passo 2: Kotlin (val com lambda, construtores) e TS (propriedade com arrow, get e set).
		{"    val handler = { e: Event -> process(e) }", "handler"},
		{"    private val onClick: (View) -> Unit = { v ->", "onClick"},
		{"    constructor(repo: Repo) : this(repo, Clock.systemUTC())", "constructor"},
		{"class OrderService(private val repo: OrderRepository) {", "OrderService"},
		{"internal data class Money(val cents: Long)", "Money"},
		{"  handle = async (req: Request) => {", "handle"},
		{"  private onSubmit = (e: FormEvent): void => {", "onSubmit"},
		{"  get total(): number {", "total"},
		{"  static set total(v: number) {", "total"},
		{"    val total = items.sumOf { it.price }", ""},
		{"  count = 0;", ""},
		// Não são declarações.
		{"        if (urlData != null && urlData.isActive()) {", ""},
		{"            cacheUrl(shortCode, urlData.getOriginalUrl(), urlData.getExpiresAt());", ""},
		{"        urlData.recordClick(ClickEvent.builder()", ""},
		{"        return Optional.of(urlData.getOriginalUrl());", ""},
		{"        } catch (Exception e) {", ""},
		{"        String cachedUrl = getCachedUrl(shortCode);", ""},
		{"    private final Map<String, UrlData> urlMappings = new ConcurrentHashMap<>();", ""},
		{"        foo(bar,", ""},
		{"// func Comentario() {", ""},
	}
	for _, c := range cases {
		if got := declName(c.line); got != c.want {
			t.Errorf("declName(%q) = %q, quer %q", c.line, got, c.want)
		}
	}
}

func TestEnclosingSymbolOnRealFile(t *testing.T) {
	idx := newSourceIndex(appJavaCheckout)
	cases := []struct {
		line int
		want string
	}{
		{154, "getOriginalUrl"}, // dentro de um if do método
		{262, "deleteUrl"},
		{273, "deleteCacheUrl"}, // dentro do catch
		{258, "deleteUrl"},      // a própria declaração
		{37, ""},                // campo da classe
	}
	for _, c := range cases {
		if got := idx.locate(signals.Evidence{File: svcFile, Line: c.line}).Symbol; got != c.want {
			t.Errorf("linha %d: símbolo %q, quer %q", c.line, got, c.want)
		}
	}
	go1 := []string{"package x", "", "func A() {", "\tif x {", "\t\ty()", "\t}", "}", "", "var z = 1"}
	if got := enclosingSymbol(go1, 5); got != "A" {
		t.Errorf("Go: %q", got)
	}
	if got := enclosingSymbol(go1, 9); got != "" {
		t.Errorf("Go fora de função: %q", got)
	}
}

func TestRelocateBySnippet(t *testing.T) {
	idx := newSourceIndex(appJavaCheckout)
	// Evidência gravada quando o deleteCacheUrl estava 10 linhas acima: o trecho a reencontra.
	l := idx.locate(signals.Evidence{File: svcFile, Line: 263, Snippet: `log.warn("Failed to delete URL: {} -> {}", shortCode, e.getMessage());`})
	if l.Line != 273 || l.Symbol != "deleteCacheUrl" {
		t.Errorf("relocação: %+v", l)
	}
	// Trecho curto não reloca.
	if l := idx.locate(signals.Evidence{File: svcFile, Line: 263, Snippet: "}"}); l.Line != 263 {
		t.Errorf("trecho curto relocou: %+v", l)
	}
	// Linha 1 é o arquivo inteiro; caminho com ".." não lê nada.
	if l := idx.locate(signals.Evidence{File: svcFile, Line: 1, Snippet: "mapa em memória"}); l.Line != 0 || l.Symbol != "" {
		t.Errorf("linha 1: %+v", l)
	}
	if l := idx.locate(signals.Evidence{File: "../../../etc/passwd", Line: 5}); l.Symbol != "" {
		t.Errorf("caminho fora do checkout: %+v", l)
	}
}

// ex9Discarded é o achado que o Judge descartou na rodada 7; ex9Rewritten é o mesmo problema,
// reescrito, que voltou na rodada 8 e foi publicado como EX-9 (ADR 0015). Textos reais.
var (
	ex9Discarded = signals.Finding{
		Repo: "acme/app-java-angular", Module: svcModule, Category: "bug", Fingerprint: "09d0ee368625",
		Title:   "Unifique a fonte de verdade das URLs entre memória e Redis",
		Context: "O resumo do serviço indica que as URLs são encurtadas e guardadas em memória (UrlData com contadores e eventos por URL), enquanto o Redis serve apenas de cache com TTL. O docker-compose configura o Redis com persistência AOF, o que sugere que o Redis deveria reter os dados. Após reinício do backend, ou com mais de uma réplica, os links e os cliques somem ou divergem, apesar da persistência paga no Redis. A responsabilidade de armazenar o estado está espalhada entre model, service e a infraestrutura, sem um dono único.",
		Evidence: []signals.Evidence{
			{File: svcFile, Line: 1, Snippet: "armazenamento de UrlData em memória com cache Redis"},
			{File: "docker-compose.yml", Line: 1, Snippet: "redis com persistência AOF"},
			{File: "backend/src/main/java/com/example/app/model/UrlData.java", Line: 1, Snippet: "contadores e eventos de cliques em memória por URL"},
		},
	}
	ex9Rewritten = signals.Finding{
		Repo: "acme/app-java-angular", Module: svcModule, Category: "bug", Fingerprint: "04bf9e71b66f",
		Title:   "Persista as URLs curtas no Redis em vez da memória do processo",
		Context: "O compose configura o Redis com persistência AOF, o que sugere que os dados sobrevivem a reinícios. Mas o UrlShortenerService mantém a fonte de verdade das URLs (mapa em memória) no processo da JVM, e o Redis guarda só o cache de redirecionamento. Depois de um restart do backend, ou com mais de uma réplica, os links criados somem ou divergem, embora o cache no Redis ainda possa apontar para eles. O job de limpeza em config também só limpa a instância local. O comportamento real contradiz a infraestrutura declarada, e nenhum pacote isolado vê as duas pontas.",
		Evidence: []signals.Evidence{
			{File: svcFile, Line: 1, Snippet: "mapa em memória de UrlData como armazenamento principal, Redis apenas como cache de redirecionamento"},
			{File: "docker-compose.yml", Line: 1, Snippet: "redis com persistência AOF habilitada"},
		},
	}
	// Rodada 11: a terceira versão, agora com linhas reais, depois de a EX-9 ser recusada.
	ex9Third = signals.Finding{
		Repo: "acme/app-java-angular", Module: svcModule, Category: "bug", Fingerprint: "8f7e2b3b2b37",
		Title: "Persistir as URLs no Redis em vez de mapa em memória como fonte de verdade",
		Evidence: []signals.Evidence{
			{File: svcFile, Line: 37, Snippet: "private final Map<String, UrlData> urlMappings = new ConcurrentHashMap<>();"},
			{File: "docker-compose.yml", Line: 7, Snippet: `command: ["redis-server", "--appendonly", "yes"]`},
			{File: "backend/src/main/java/com/example/app/config/CleanupScheduler.java", Line: 16, Snippet: "public void cleanupExpiredUrls() {"},
		},
	}
)

func evidenceJSON(t *testing.T, ev []signals.Evidence) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func findSimilar(sims []judge.Similar, fp string) (judge.Similar, bool) {
	for _, s := range sims {
		if s.Fingerprint == fp {
			return s, true
		}
	}
	return judge.Similar{}, false
}

// EX-9: a recusa que voltou com outras palavras. A similaridade léxica não a pega em código, e
// hoje só a levaria ao Judge por estar acima de 0,3; a localização a leva ao Judge pelo lugar,
// independente das palavras.
func TestEX9RewordedDiscardReachesJudgeByLocation(t *testing.T) {
	h := HashEmbedder{}
	sim := Cosine(h.Embed(ex9Discarded, ex9Discarded.Title), h.Embed(ex9Rewritten, ex9Rewritten.Title))
	t.Logf("similaridade léxica entre o descarte da rodada 7 e a EX-9: %.3f", sim)
	if sim >= 0.92 {
		t.Fatalf("o caso deixou de ser uma reescrita: léxica %.3f já pegaria", sim)
	}

	prior := priorDecisions{list: []priorDecision{{
		RunID: 7, Fingerprint: ex9Discarded.Fingerprint, Title: ex9Discarded.Title, Category: "bug",
		Decision: judge.Discard, Module: ex9Discarded.Module, Evidence: ex9Discarded.Evidence,
	}}}
	// Concorrentes reais no mesmo service: outros problemas, abertos (EX-6 e EX-7).
	rows := []db.LocationCandidatesRow{
		{Fingerprint: "f1d40bb6ddcf", Title: "Impede que redirect concorrente recacheie URL deletada", Category: "bug", State: "open", Module: svcModule,
			Evidence: evidenceJSON(t, []signals.Evidence{{File: svcFile, Line: 154}, {File: svcFile, Line: 261}, {File: svcFile, Line: 262}})},
		{Fingerprint: "0dfacd4a87f8", Title: "Limita o crescimento de memória de URLs e cliques", Category: "performance", State: "open", Module: svcModule,
			Evidence: evidenceJSON(t, []signals.Evidence{{File: svcFile, Line: 284}, {File: svcFile, Line: 176}, {File: svcFile, Line: 37}})},
		{Fingerprint: "dbb8d0b56e18", Title: "Outro módulo, mesmo nome de arquivo não importa", Category: "bug", State: "rejected", Module: "backend/src/main/java/com/example/app/dto",
			Evidence: evidenceJSON(t, []signals.Evidence{{File: "docker-compose.yml", Line: 1}})},
	}
	pool, err := buildLocationPool(newSourceIndex(appJavaCheckout), 10, 5, rows, prior, nil)
	if err != nil {
		t.Fatal(err)
	}
	sims, hits := pool.attach(ex9Rewritten, nil)
	got, ok := findSimilar(sims, ex9Discarded.Fingerprint)
	if !ok {
		t.Fatalf("o descarte da rodada 7 não chegou ao Judge: %+v", hits)
	}
	if got.State != "discarded" || got.Similarity != 0 {
		t.Errorf("similar do descarte: %+v", got)
	}
	want := []string{"mesmo arquivo " + svcFile, "mesmo arquivo docker-compose.yml"}
	if strings.Join(got.Location, "|") != strings.Join(want, "|") {
		t.Errorf("motivos = %q, quer %q", got.Location, want)
	}
	if hits[0].Fingerprint != ex9Discarded.Fingerprint {
		t.Errorf("o descarte, com dois arquivos em comum, deveria vir primeiro: %+v", hits)
	}
	if _, ok := findSimilar(sims, "dbb8d0b56e18"); ok {
		t.Error("candidato de outro módulo entrou")
	}
	t.Logf("candidatos por localização: %+v", hits)
}

// Rodada 11: depois da recusa humana (com supressão pelo fingerprint), a terceira versão aponta
// para linhas reais. O fingerprint é outro, então a supressão não a barra; a localização leva a
// EX-9 suprimida ao Judge.
func TestEX9ThirdVersionReachesSuppressedFinding(t *testing.T) {
	rows := []db.LocationCandidatesRow{
		{Fingerprint: ex9Rewritten.Fingerprint, Title: ex9Rewritten.Title, Category: "bug", State: "rejected", Suppressed: true,
			Module: svcModule, Identifier: "EX-9", Evidence: evidenceJSON(t, ex9Rewritten.Evidence)},
		{Fingerprint: "0dfacd4a87f8", Title: "Limita o crescimento de memória de URLs e cliques", Category: "performance", State: "open", Module: svcModule,
			Evidence: evidenceJSON(t, []signals.Evidence{{File: svcFile, Line: 284}, {File: svcFile, Line: 176}, {File: svcFile, Line: 37}})},
	}
	pool, err := buildLocationPool(newSourceIndex(appJavaCheckout), 10, 5, rows, priorDecisions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// O similar léxico que já estava na lista ganha o motivo, sem duplicar.
	sims, hits := pool.attach(ex9Third, []judge.Similar{{Fingerprint: "0dfacd4a87f8", Title: "Limita o crescimento de memória de URLs e cliques", State: "open", Similarity: 0.41}})
	if len(sims) != 2 || len(hits) != 2 {
		t.Fatalf("similares = %+v", sims)
	}
	if sims[0].Similarity != 0.41 || len(sims[0].Location) == 0 || !strings.Contains(sims[0].Location[0], "linhas próximas") {
		t.Errorf("similar léxico sem o motivo de localização: %+v", sims[0])
	}
	ex9, ok := findSimilar(sims, ex9Rewritten.Fingerprint)
	if !ok || ex9.State != "suppressed" || ex9.Issue != "EX-9" {
		t.Errorf("EX-9 suprimida não chegou ao Judge: %+v", sims)
	}
}

// Mesmo símbolo pesa mais que mesmo arquivo: as variantes reais de "link excluído servido pelo
// cache" (rodadas 9 a 11) caem nos mesmos métodos da EX-6, em linhas diferentes.
func TestSameSymbolRanksAboveSameFile(t *testing.T) {
	rows := []db.LocationCandidatesRow{
		{Fingerprint: "f1d40bb6ddcf", Title: "Impede que redirect concorrente recacheie URL deletada", State: "open", Module: svcModule,
			Evidence: evidenceJSON(t, []signals.Evidence{{File: svcFile, Line: 154}, {File: svcFile, Line: 261}})},
		{Fingerprint: "aaaaaaaaaaaa", Title: "Outro problema no mesmo arquivo", State: "rejected", Module: svcModule,
			Evidence: evidenceJSON(t, []signals.Evidence{{File: svcFile, Line: 56}})},
	}
	pool, err := buildLocationPool(newSourceIndex(appJavaCheckout), 3, 5, rows, priorDecisions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	variant := signals.Finding{Module: svcModule, Fingerprint: "160e2024f14b", Evidence: []signals.Evidence{
		{File: svcFile, Line: 143}, {File: svcFile, Line: 266}, {File: svcFile, Line: 273},
	}}
	_, hits := pool.attach(variant, nil)
	if len(hits) != 2 || hits[0].Fingerprint != "f1d40bb6ddcf" {
		t.Fatalf("ordem: %+v", hits)
	}
	if hits[0].Score != scoreSymbolSameFile*2+scoreSameFile || hits[1].Score != 3*scoreSameFile {
		t.Errorf("pontos: %+v", hits)
	}
	if !strings.Contains(strings.Join(hits[0].Reasons, "|"), "mesmo símbolo getOriginalUrl") {
		t.Errorf("motivos: %+v", hits[0].Reasons)
	}
	// Teto e desligamento.
	pool.max = 1
	if _, hits := pool.attach(variant, nil); len(hits) != 1 {
		t.Errorf("teto não aplicado: %+v", hits)
	}
	var off *locationPool
	if sims, hits := off.attach(variant, nil); sims != nil || hits != nil {
		t.Error("pool desligado devolveu candidatos")
	}
}

// Rejudge v7 de 2026-10-10: achados mesclados na própria EX-7 (pelo Judge, e pela EX-5, que é
// duplicata humana dela) e na própria EX-6 (pela EX-8) entravam como outros achados. O Judge
// via no achado uma duplicata de uma parte dele mesmo: mesclou a EX-7 na EX-5, ao contrário da
// decisão humana, e descartou a EX-6 como "já mesclada". O que é parte do achado em julgamento,
// direta ou indiretamente, fica fora dos similares, nas duas vias.
func TestFindingsMergedIntoTheJudgedOneAreNotSimilar(t *testing.T) {
	const hay5, hay6, hay7, hay8 = "a6a13848ffff", "f1d40bb6ddcf", "0dfacd4a87f8", "9f8c1e90f2a7"
	hay7f := signals.Finding{Module: svcModule, Fingerprint: hay7, Evidence: []signals.Evidence{
		{File: svcFile, Line: 284}, {File: svcFile, Line: 176}, {File: svcFile, Line: 37},
	}}
	vec := HashEmbedder{}.Embed(hay7f, "links expirados mapa memória")
	prior := priorDecisions{list: []priorDecision{
		{Fingerprint: "3bbda6843418", Title: "Remove links expirados do mapa em memória", Decision: judge.Merge, MergedInto: hay7,
			Module: svcModule, Evidence: []signals.Evidence{{File: svcFile, Line: 284}, {File: svcFile, Line: 37}}, vec: vec},
		{Fingerprint: "5be37ecdbbb7", Title: "Limita a fila de eventos de clique em memória", Decision: judge.Merge, MergedInto: hay5,
			Module: svcModule, Evidence: []signals.Evidence{{File: svcFile, Line: 176}}, vec: vec},
		{Fingerprint: "799392b41641", Title: "Impede redirecionamento de links excluídos via cache", Decision: judge.Merge, MergedInto: hay8,
			Module: svcModule, Evidence: []signals.Evidence{{File: svcFile, Line: 284}}, vec: vec},
	}}
	merges := mergeGraph{hay5: hay7, hay8: hay6}
	for _, d := range prior.list {
		merges[d.Fingerprint] = d.MergedInto
	}
	rows := []db.LocationCandidatesRow{
		{Fingerprint: hay5, Title: "Limita a fila de eventos de clique por URL", State: "open", Merged: true,
			Module: svcModule, Evidence: evidenceJSON(t, []signals.Evidence{{File: svcFile, Line: 176}})},
	}
	pool, err := buildLocationPool(newSourceIndex(appJavaCheckout), 10, 5, rows, prior, merges)
	if err != nil {
		t.Fatal(err)
	}
	sims, _ := pool.attach(hay7f, nil)
	near := prior.nearest(vec, 3, hay7, merges)
	for _, fp := range []string{"3bbda6843418", "5be37ecdbbb7", hay5} {
		if _, ok := findSimilar(sims, fp); ok {
			t.Errorf("%s é parte da EX-7 e entrou por localização", fp)
		}
		if _, ok := findSimilar(near, fp); ok {
			t.Errorf("%s é parte da EX-7 e entrou pela via léxica", fp)
		}
	}
	// A mescla na EX-8 (que é duplicata da EX-6) não é parte da EX-7: continua candidata.
	if _, ok := findSimilar(sims, "799392b41641"); !ok {
		t.Error("mescla em outro achado deveria continuar como candidata por localização")
	}
	if _, ok := findSimilar(near, "799392b41641"); !ok {
		t.Error("via léxica perdeu a mescla em outro achado")
	}
	// Julgando a EX-6, a mesma mescla é parte dela, pela EX-8.
	if !merges.partOf("799392b41641", hay6) || merges.partOf(hay6, "799392b41641") {
		t.Error("cadeia 799392b41641 → EX-8 → EX-6 mal resolvida")
	}
	// Ciclo não trava.
	if (mergeGraph{"a": "b", "b": "a"}).partOf("a", "c") {
		t.Error("ciclo")
	}
}

func TestMergeTarget(t *testing.T) {
	cases := []struct {
		name, decision, mergeWith string
		dir                       *MergeChoice
		want                      string
	}{
		{"sem direção vale o merge_with", judge.Merge, "b", nil, "b"},
		{"absorvido pela direção", judge.Merge, "b", &MergeChoice{Absorber: "b", Absorbed: "a"}, "b"},
		{"absorvedor não foi mesclado", judge.Merge, "b", &MergeChoice{Absorber: "a", Absorbed: "b"}, ""},
		{"descarte não é mescla", judge.Discard, "", nil, ""},
	}
	for _, c := range cases {
		if got := mergeTarget("a", c.decision, c.mergeWith, c.dir); got != c.want {
			t.Errorf("%s: %q, quer %q", c.name, got, c.want)
		}
	}
}

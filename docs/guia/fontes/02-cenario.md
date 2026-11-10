# Escolha o seu cenário

Os três modos usam o mesmo Postgres e o mesmo histórico. Dá para começar no local e migrar depois sem perder nada.

<div class="cards">
<a class="card" href="#instalacao-local">
<span class="pill">modo local</span>
<h4>Quero avaliar, sem nenhuma chave</h4>
<p>Uma pessoa, um repo, uma tarde. Os achados saem num relatório na sua máquina; você decide pela linha de comando, e o PR sai pela sua conta do <code>gh</code>.</p>
<p>Precisa de: Docker, Go, <code>claude</code> e <code>gh</code> logados.</p>
<p class="go">Instalar no modo local →</p>
</a>
<a class="card" href="#instalacao-mcp">
<span class="pill">modo MCP</span>
<h4>Piloto com um ou dois times</h4>
<p>O time decide no Linear, sem criar apps: o Sentinela usa o MCP do Linear do seu Claude Code, e tudo sai com a sua conta.</p>
<p>Precisa de: o mesmo, mais o Linear (e, se quiser, o Slack) conectado no Claude Code.</p>
<p class="go">Instalar no modo MCP →</p>
</a>
<a class="card" href="#uso-em-empresa">
<span class="pill">modo completo</span>
<h4>Empresa, uso real</h4>
<p>Identidade própria: app OAuth no Linear, bot no Slack, duas GitHub Apps. Webhook, canal e menção por time, e auto-aprovação onde a organização liberar.</p>
<p>Precisa de: os admins do Linear, do Slack e do GitHub, e um servidor.</p>
<p class="go">Ler os requisitos e instalar →</p>
</a>
</div>

O caminho recomendado numa empresa passa pelos três: local num repo, piloto MCP com um ou dois times e, depois, o modo completo, com os times entrando um a um. Ele está detalhado em [Uso em empresa](#uso-em-empresa).

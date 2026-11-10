package main

const page = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>DEXSTER // NEURAL COMMAND</title><style>
:root{color-scheme:dark;--bg:#080b14;--panel:#101827;--border:#263b50;--cyan:#64f1df;--orange:#ff9869;--muted:#8ea0b7}*{box-sizing:border-box}body{margin:0;font:14px system-ui,'Segoe UI',sans-serif;background:radial-gradient(ellipse at 80% 0%,#192941 0%,var(--bg) 55%);color:#e5f4fc}header{padding:24px 32px;border-bottom:1px solid var(--border);display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap}h1{margin:0;font-size:26px;letter-spacing:2px;font-weight:750}h1 span{color:var(--cyan)}small,.hint{color:var(--muted)}main{padding:24px;display:grid;grid-template-columns:minmax(310px,410px) minmax(350px,1fr);gap:18px;max-width:1600px;margin:auto}.panel{background:#101827dd;border:1px solid var(--border);border-radius:14px;padding:20px;box-shadow:0 12px 35px #0004}.panel h2{font-size:15px;color:var(--cyan);letter-spacing:1px;margin:0 0 16px}label{display:block;font-size:12px;letter-spacing:.4px;color:var(--muted);margin:14px 0 6px}input,textarea,select{width:100%;background:#080f1d;border:1px solid #31475d;color:#edf7ff;border-radius:8px;padding:10px;font:inherit}textarea{min-height:140px;resize:vertical}input:focus,textarea:focus{outline:1px solid var(--cyan)}.two{display:grid;grid-template-columns:1fr 1fr;gap:12px}.checks{display:flex;flex-wrap:wrap;gap:16px;margin:16px 0}.checks label{margin:0;color:#d3e6f3}.checks input{width:auto;margin-right:6px}button{padding:12px 18px;border:0;border-radius:9px;font-weight:bold;cursor:pointer;background:var(--cyan);color:#061420}button.stop{background:#ff876f}button:disabled{opacity:.4;cursor:not-allowed}.controls{display:flex;gap:10px}.status{display:inline-flex;align-items:center;border:1px solid var(--border);padding:8px 14px;border-radius:40px;color:var(--cyan);letter-spacing:1px}pre{background:#060b14;border:1px solid #263548;border-radius:9px;padding:16px;color:#a8eace;font:12px/1.55 ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;height:64vh;min-height:380px;overflow:auto}.terminal-panes{display:grid;grid-template-rows:minmax(240px,1fr) minmax(240px,1fr);gap:12px;height:70vh;min-height:510px}.terminal-pane{min-height:0;display:flex;flex-direction:column}.terminal-pane label{margin:3px 0 7px;display:flex;align-items:center;gap:8px}.terminal-pane pre{flex:1;min-height:0;height:auto;margin:0;white-space:pre;overflow:auto;overflow-wrap:normal}.terminal-pane pre.model-output{color:#d3e9ff}.terminal-pane small{color:var(--muted)}.stats{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}.stat{padding:14px;background:#091320;border:1px solid var(--border);border-radius:10px}.stat b{display:block;font-size:22px;color:var(--orange)}.stat small{font-size:11px}.message{min-height:24px;margin:12px 0;color:var(--orange)}.right-stack{display:flex;flex-direction:column;gap:18px;min-width:0}.analyst-preview{height:auto;min-height:130px;max-height:290px;overflow:auto;white-space:pre-wrap}.analyst-repos{display:flex;flex-direction:column;gap:6px;margin:8px 0}.analyst-repos a{color:var(--cyan);overflow-wrap:anywhere}.analyst-toggle{display:flex;align-items:center;gap:8px;margin:8px 0;color:#e5f4fc}.analyst-toggle input{width:auto}.analyst-panel{border-color:#37696f}footer{margin:12px 0;color:var(--muted)}@media(max-width:850px){main{grid-template-columns:1fr}pre{height:45vh}.terminal-panes{height:65vh;min-height:480px}} 
/* DEXSTER LOWER TERMINAL WIDTH FIX v1 */
html,body{max-width:100%;overflow-x:hidden}
main,.panel,.right-stack,.terminal-panes,.terminal-pane{min-width:0;max-width:100%}
main{grid-template-columns:minmax(0,410px) minmax(0,1fr)}
.terminal-panes{width:100%;overflow:hidden}
.terminal-pane{width:100%;overflow:hidden}
.terminal-pane pre,.terminal-pane pre.model-output{
  display:block;box-sizing:border-box;width:100%;max-width:100%;min-width:0;
  white-space:pre-wrap;overflow-wrap:anywhere;word-break:normal;
  overflow-x:auto;overflow-y:auto;contain:inline-size;
  font-family:ui-monospace,SFMono-Regular,Consolas,monospace;
  tab-size:4;
}
@media(max-width:850px){main{grid-template-columns:minmax(0,1fr)}}
</style></head><body>
<header><div><h1>DEXSTER <span>//</span> NEURAL COMMAND</h1><small>LOCAL AGENT CONTROL · KRAKEN MEMORY · v30 ENGINE</small></div><strong id="status" class="status">IDLE</strong></header><main><section class="panel"><h2>01 / TASK CONTROL</h2>
<label>Среда выполнения</label><select id="runtime"><option value="worker">Изолированный Worker · dexster-worker (Ollama через Model Bridge)</option><option value="host" selected>HOST · без песочницы (опасно, только доверенные проекты)</option></select><div style="margin-top:8px"><button type="button" id="workerProbe">◎ Проверить песочницу</button> <small id="workerProbeResult">Проверка не запускалась</small></div><small id="runtimeHint">Worker: копия проекта, Bubblewrap без сети, Ollama через локальный мост. Оригинал не изменяется автоматически. HOST — только доверенный код.</small>
<label>Путь исходного проекта</label><div style="display:flex;gap:8px"><input id="root" placeholder="/home/monk/Desktop/my-project"><button type="button" id="chooseProject" style="white-space:nowrap">📁 Выбрать / Создать</button></div>
<div id="savedProjectsPanel" style="margin:10px 0;padding:10px;border:1px solid #31475d;border-radius:10px;background:#091320">
 <div style="display:flex;gap:8px;align-items:center;justify-content:space-between"><strong>📂 Сохранённые проекты</strong><button type="button" id="refreshSavedProjects" style="padding:6px 10px;font-size:12px">↻ Обновить</button></div>
 <small id="savedProjectsBase">~/DEXSTER-Projects · только чтение</small>
 <div id="savedProjectsList" style="max-height:220px;overflow:auto;display:grid;gap:6px;margin-top:9px"><small>Нажми «Обновить» для загрузки.</small></div>
 <small id="savedProjectsError" style="display:block;color:#ff9869;margin-top:5px"></small>
</div>
<div id="projectChooser" hidden style="margin-top:10px;padding:12px;border:1px solid #31475d;border-radius:10px;background:#091320">
 <div style="display:flex;gap:8px;align-items:center;margin-bottom:8px"><button type="button" id="folderUp">↑</button><strong id="folderCurrent" style="overflow-wrap:anywhere;flex:1"></strong><button type="button" id="folderClose">✕</button></div>
 <div id="folderList" style="max-height:240px;overflow:auto;display:grid;gap:5px"></div>
 <div style="display:flex;gap:8px;margin-top:10px"><input id="newFolderName" placeholder="Имя новой папки"><button type="button" id="makeFolder" style="white-space:nowrap">+ Создать папку</button></div>
 <button type="button" id="useFolder" style="width:100%;margin-top:10px">✓ Выбрать эту папку</button>
 <small id="folderError" style="display:block;color:#ff9869;margin-top:6px"></small>
</div>
<label>KRAKEN (необязательно, создастся рядом)</label><input id="kraken" placeholder="Папка памяти">
<label>Провайдер модели</label><select id="provider"><option value="ollama">Ollama — локально</option><option value="openai">OpenAI API — облако</option><option value="llamacpp">llama.cpp — локальный API</option><option value="unsloth">Unsloth Studio — локальный API</option><option value="strata">STRATA — локальный API</option></select>
<label>Модель</label><select id="modelSelect"></select><input id="model" value="qwen3.8:27b-iq4xs" autocomplete="off" placeholder="Модель OpenAI API" style="margin-top:8px" hidden><div style="display:flex;gap:12px;align-items:center;margin-top:8px"><button id="refreshModels" type="button" style="padding:7px 11px;font-size:12px">↻ Модели Ollama</button><small id="modelHint">Загрузка списка...</small></div><small id="providerHint">Для llama.cpp: DEXSTER_LLAMACPP_URL (по умолчанию http://127.0.0.1:8080/v1). Для Unsloth: DEXSTER_UNSLOTH_URL (обязательно). API-ключи задаются на сервере.</small>
<div id="strataOptions" hidden><label>STRATA API (отдельно от llama.cpp)</label><input id="strataURL" value="http://127.0.0.1:47831/v1" autocomplete="off" placeholder="http://127.0.0.1:47831/v1"><small>Используй порт STRATA из её работающего сервера. DEXSTER не запускает и не перенастраивает STRATA.</small></div>
<label>Задача</label><textarea id="task" placeholder="Что исправить, какие тесты пройти, что сохранить..."></textarea>
<div class="two"><div><label>Контекст</label><input id="ctx" type="number" min="1024" value="16384"></div><div><label>Бюджет промпта</label><input id="budget" type="number" min="0" value="9000"></div><div><label>Ротация после токенов</label><input id="rotate" type="number" min="0" value="13926"></div><div><label>Макс. итераций</label><input id="iterations" type="number" min="1" value="35"></div></div>
<div class="checks"><label><input id="commit" type="checkbox" checked>Git commit</label><label><input id="verify" type="checkbox" checked>Auto verify</label><label id="unloadRow"><input id="unload" type="checkbox">Unload on rotate</label><label><input id="http" type="checkbox">HTTP Gate</label></div>
<div id="httpOpts" hidden><label>HTTP URL</label><input id="url" value="http://127.0.0.1:8080/"><label>Текст ответа</label><input id="contains" placeholder="NEON FORGE"></div><div class="controls" style="flex-wrap:wrap;align-items:center"><button id="start">▶ START / RESUME</button><button id="stop" class="stop" disabled>■ STOP</button><span id="llamaManage" hidden style="display:inline-flex;align-items:center;gap:8px;flex-wrap:wrap"><button type="button" id="llamaDownload" style="padding:10px 12px">↓ Скачать GGUF</button><button type="button" id="llamaLoad" style="padding:10px 12px">⬆ Загрузить</button><button type="button" id="llamaUnload" class="stop" style="padding:10px 12px">⬇ Выгрузить</button><span id="llamaDot" title="Модель не загружена" style="display:inline-block;width:13px;height:13px;border-radius:50%;background:#6a7787"></span><small id="llamaStatus" style="min-width:85px">Не проверено</small></span><span id="strataManage" hidden style="display:none;align-items:center;gap:8px;flex-wrap:wrap"><button type="button" id="strataLoad" style="padding:10px 12px">⬆ Загрузить STRATA</button><button type="button" id="strataUnload" class="stop" style="padding:10px 12px">⬇ Выгрузить STRATA</button><span id="strataDot" title="Состояние STRATA" style="display:inline-block;width:13px;height:13px;border-radius:50%;background:#6a7787"></span><small id="strataStatus" style="min-width:85px">Не проверено</small><small>Только вручную · после DONE модель остаётся загруженной</small></span></div><div id="msg" class="message"></div><footer>Работает только на 127.0.0.1. START с тем же проектом и задачей использует сохранённое состояние DEXSTER.</footer></section>
<div class="right-stack"><section class="panel analyst-panel"><h2>02 / МОДЕЛЬ-АНАЛИТИК · PLANNER</h2>
<small>Правая панель · отдельная локальная модель читает задачу, составляет план и ищет открытые аналоги. Код пишет только основная модель DEXSTER.</small>
<label class="analyst-toggle"><input type="checkbox" id="analystToggle" checked> Включить Аналитика перед START</label>
<div class="two"><div><label>Провайдер Аналитика</label><select id="analystProvider"><option value="ollama">Ollama</option><option value="llamacpp">llama.cpp</option><option value="strata">STRATA</option></select><label>Модель Аналитика</label><input id="analystModel" list="analystModels" placeholder="Например, установленная модель 4B" autocomplete="off"><datalist id="analystModels"></datalist></div><div><label>Контекст Аналитика</label><input id="analystCtx" type="number" min="4096" max="32768" value="8192"></div></div>
<small id="analystModelHint">Выбери модель выбранного провайдера. Аналитик работает отдельно от исполнителя.</small>
<div class="controls" style="margin-top:12px"><button id="analyzeBtn" type="button">◎ Проанализировать задачу</button></div>
<div id="analystMsg" class="message">План ещё не подготовлен.</div>
<pre id="analystPlan" class="analyst-preview">Аналитик выдаст этапы, критерии готовности и сохранит план в KRAKEN.</pre>
<small id="analystResearch">Поиск open-source аналогов: отправляются только короткие поисковые фразы в GitHub Search API, не файлы проекта.</small>
<div class="analyst-repos" id="analystRepos"></div></section>
<section class="panel"><h2>03 / LIVE TELEMETRY</h2><div class="stats"><div class="stat"><b id="calls">—</b><small>LLM calls</small></div><div class="stat"><b id="tokens">—</b><small>Total tokens</small></div><div class="stat"><b id="iter">—</b><small>Iterations</small></div></div><div class="terminal-panes"><div class="terminal-pane"><label>КОНТРОЛЛЕР · LIVE ACTIVITY</label><pre id="log">Ожидание задачи...</pre></div><div class="terminal-pane"><label>МОДЕЛЬ · ЖИВОЙ ВЫВОД КОДА</label><pre id="modelLog" class="model-output">Ответы Qwen появятся после запуска. При write_file здесь будет код файла.</pre></div></div><small id="meta">Оба окна обновляются независимо.</small><hr style="border:0;border-top:1px solid #263b50;margin:24px 0"><h2>04 / REVIEW & APPLY</h2><small>Изменения из Worker → просмотр → твоё подтверждение → исходный проект</small><div class="controls" style="margin-top:12px"><button type="button" id="reviewBtn">◎ Просмотреть diff</button><button type="button" id="applyBtn" disabled>✓ Применить изменения</button></div><small id="reviewMessage">Доступно после DONE и проверенного Git-коммита. Для задач, начатых в v2.8, требуется новый запуск в v2.9.</small><pre id="reviewDiff" style="min-height:110px;height:230px">Ожидание проверки...</pre><hr style="border:0;border-top:1px solid #263b50;margin:24px 0"><h2>05 / KRAKEN MEMORY</h2><small id="memoryRoot">Память появится после запуска задачи.</small><div class="stats" style="margin-top:12px"><div class="stat"><b id="evCount">0</b><small>Events</small></div><div class="stat"><b id="cpCount">0</b><small>Checkpoints</small></div><div class="stat"><b id="decCount">0</b><small>Decisions</small></div></div><small id="memoryInfo">Только чтение · файлы памяти не изменяются</small></section></div></main>
<script>
const $=id=>document.getElementById(id);let secret='';let lastLog='';let lastModelLog='';let running=false;
const n=id=>Number($(id).value), v=id=>$(id).value;
let folderSelected='';
async function showFolder(path=''){
  if(!secret)await loadToken();
  $('folderError').textContent='';
  const r=await fetch('/api/project-folders?path='+encodeURIComponent(path),{headers:{'X-Dexster-Token':secret},cache:'no-store'});
  if(!r.ok)throw Error(await r.text());
  const data=await r.json();folderSelected=data.path;
  $('folderCurrent').textContent=data.path;
  $('folderUp').onclick=()=>showFolder(data.parent).catch(e=>$('folderError').textContent=e.message);
  const list=$('folderList');list.replaceChildren();
  for(const d of data.folders||[]){
    const b=document.createElement('button');b.type='button';b.textContent='📁 '+d.name;
    b.style.cssText='text-align:left;background:#162c3c;color:#e5f4fc';
    b.onclick=()=>showFolder(d.path).catch(e=>$('folderError').textContent=e.message);
    list.append(b);
  }
}
$('chooseProject').onclick=async()=>{
  $('projectChooser').hidden=false;
  try{await showFolder(v('root'))}catch(e){try{await showFolder()}catch(e2){$('folderError').textContent=e2.message}}
};
$('folderClose').onclick=()=>$('projectChooser').hidden=true;
$('useFolder').onclick=()=>{
  if(!folderSelected)return;
  $('root').value=folderSelected;$('root').dispatchEvent(new Event('input'));
  $('projectChooser').hidden=true;
};
let latestProjectAutoSelectV32=false;
async function refreshSavedProjects(){
  const list=$('savedProjectsList');$('savedProjectsError').textContent='';
  list.textContent='Загрузка проектов...';
  try{
    if(!secret)await loadToken();
    const r=await fetch('/api/saved-projects',{headers:{'X-Dexster-Token':secret},cache:'no-store'});
    if(!r.ok)throw Error(await r.text());
    const data=await r.json();$('savedProjectsBase').textContent=data.base;
    list.replaceChildren();
    if(!data.projects||!data.projects.length){list.textContent='Проектов пока нет';return;}
    if(!latestProjectAutoSelectV32 && !running){
      latestProjectAutoSelectV32=true;
      const latest=data.projects[0];
      if(latest && latest.saved_state){$('root').value=latest.path;$('root').dispatchEvent(new Event('input'));}
    }
    for(const p of data.projects){
      const box=document.createElement('div');box.style.cssText='padding:8px;border:1px solid #263b50;border-radius:8px;display:flex;align-items:center;gap:7px';
      const info=document.createElement('div');info.style.cssText='flex:1;min-width:0;overflow-wrap:anywhere';
      const name=document.createElement('strong');name.textContent=p.name;name.style.fontSize='12px';
      const status=document.createElement('small');status.style.display='block';
      status.textContent=p.status==='completed'?'● Готов · VERIFIED':p.status==='incomplete'?'● Не завершён / требуется проверка':'○ Новая папка';
      status.style.color=p.status==='completed'?'#53d68c':p.status==='incomplete'?'#ff7474':'#9cacc1';
      box.style.borderColor=p.status==='completed'?'#276b4c':p.status==='incomplete'?'#773b46':'#263b50';
      info.append(name,status);
      const btn=document.createElement('button');btn.type='button';btn.textContent='Открыть';
      btn.style.cssText='padding:7px 10px;font-size:12px';
      btn.onclick=()=>{if(running){$('savedProjectsError').textContent='Сначала останови активную задачу';return;}
        $('root').value=p.path;$('root').dispatchEvent(new Event('input'));
        $('savedProjectsError').textContent='Путь выбран. Для той же задачи используй штатный START / RESUME.';
      };
      box.append(info,btn);list.append(box);
    }
  }catch(e){list.textContent='';$('savedProjectsError').textContent='Список недоступен: '+e.message;}
}
$('refreshSavedProjects').onclick=refreshSavedProjects;
loadToken().then(refreshSavedProjects).catch(()=>{});
$('makeFolder').onclick=async()=>{
  try{
    if(!secret)await loadToken();
    const r=await fetch('/api/project-folders/mkdir',{method:'POST',headers:{'Content-Type':'application/json','X-Dexster-Token':secret},body:JSON.stringify({parent:folderSelected,name:v('newFolderName')})});
    if(!r.ok)throw Error(await r.text());
    const data=await r.json();$('newFolderName').value='';await showFolder(data.path);
  }catch(e){$('folderError').textContent=e.message}
};

// While Ollama is still composing its constrained JSON tool call, display
// the content string as source code rather than the JSON wire format.
// This is display-only; the Go controller still parses the FINAL response.
function liveModelPreview(log){
  const pos=log.lastIndexOf('── МОДЕЛЬ · ВЫЗОВ');
  if(pos<0)return log;
  const part=log.slice(pos);
  if(!/"name"\s*:\s*"write_file"/.test(part))return log;
  const cm=/"content"\s*:\s*"/.exec(part);
  if(!cm)return log;
  const pm=/"path"\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"/.exec(part);
  const file=pm?pm[1]:'ожидание пути';
  const raw=part.slice(cm.index+cm[0].length);
  let code='';
  for(let i=0;i<raw.length;i++){
    const ch=raw[i];
    if(ch==='"')break;
    if(ch!=='\\'){code+=ch;continue}
    if(i+1>=raw.length)break;
    const esc=raw[++i];
    if(esc==='n')code+='\n';
    else if(esc==='r')code+='\r';
    else if(esc==='t')code+='\t';
    else if(esc==='b')code+='\b';
    else if(esc==='f')code+='\f';
    else if(esc==='u'){
      const hex=raw.slice(i+1,i+5);
      if(!/^[0-9a-fA-F]{4}$/.test(hex))break;
      code+=String.fromCharCode(parseInt(hex,16));i+=4;
    }else if(esc==='"'||esc==='\\'||esc==='/')code+=esc;
    else break;
  }
  return log.slice(0,pos)+'── МОДЕЛЬ · НАБОР КОДА ──\nФАЙЛ: '+file+'\n'+code;
}

let preparedPlan='';
function setAnalystModels(names){
  $('analystModels').replaceChildren(...names.map(name=>new Option(name,name)));
  if(!v('analystModel')){
    const small=names.find(x=>/(?:^|[^0-9])(?:1|2|3|4|7|8)(?:\.\d)?b(?:[^a-z0-9]|$)/i.test(x)&&x!==v('model'));
    if(small)$('analystModel').value=small;
  }
  $('analystModelHint').textContent=v('analystModel')?'Аналитик: '+v('analystModel')+' · '+v('analystProvider'):'Выбери установленную модель Аналитика';
}
function clearAnalystPlan(){preparedPlan='';$('analystMsg').textContent='План нужно обновить перед стартом';}
async function analyzeTask(){
  if(!v('analystModel'))throw Error('Укажи модель Аналитика');
  if(!secret)await loadToken();
  $('analystMsg').textContent='Аналитик составляет план и проверяет GitHub...';
  $('analyzeBtn').disabled=true;
  try {
    const r=await fetch('/api/analyst/plan',{method:'POST',headers:{'Content-Type':'application/json','X-Dexster-Token':secret},body:JSON.stringify({Task:v('task'),Root:v('root'),Kraken:v('kraken'),Model:v('model'),AnalystModel:v('analystModel'),AnalystProvider:v('analystProvider'),AnalystContext:n('analystCtx'),StrataURL:v('strataURL')})});
    if(!r.ok)throw Error(await r.text());
    const plan=await r.json();preparedPlan=plan.id;
    $('analystPlan').textContent=(plan.goal||'')+'\n\n'+(plan.steps||[]).map((s,i)=>(i+1)+'. '+s.title+'\n   '+s.action+'\n   Готово: '+s.done_when).join('\n\n')+'\n\nОграничения: '+(plan.constraints||[]).join('; ');
    $('analystResearch').textContent=plan.research_status||'Поиск не выполнялся';
    const refs=$('analystRepos');refs.replaceChildren();
    for(const entry of plan.references||[]){try{const u=new URL(entry.url);if(u.protocol!=='https:'||u.hostname!=='github.com')continue;const a=document.createElement('a');a.href=u.href;a.target='_blank';a.rel='noopener noreferrer';a.textContent=entry.name||u.pathname;refs.appendChild(a)}catch(_){}}
    $('analystMsg').textContent='План готов · '+(plan.steps||[]).length+' этапов · '+(plan.references||[]).length+' GitHub-ссылок';
    return plan;
  }catch(e){preparedPlan='';$('analystMsg').textContent='АНАЛИТИК: '+e.message;throw e}
  finally{$('analyzeBtn').disabled=false}
}
$('analyzeBtn').onclick=()=>analyzeTask().catch(()=>{});
for(const id of ['task','root','kraken','model','analystModel','analystProvider','strataURL'])$(id).addEventListener('input',clearAnalystPlan);
$('analystToggle').addEventListener('change',()=>{$('analystMsg').textContent=$('analystToggle').checked?'Аналитик включён':'Аналитик отключён: основная модель получит только исходное задание'});

async function refreshModels(){
  const h=$('modelHint');h.textContent='Загрузка моделей...';
  $('modelSelect').replaceChildren(new Option('Загрузка...', ''));
  $('modelSelect').value='';
  $('model').value='';
  try{
    if(!secret)await loadToken();const provider=v('provider'),llama=provider==='llamacpp',strata=provider==='strata';
    const url=strata?'/api/strata/models?url='+encodeURIComponent(v('strataURL')):(llama?'/api/llamacpp/models':'/api/models');
    const r=await fetch(url,{headers:(llama||strata)?{'X-Dexster-Token':secret}:{},cache:'no-store'});if(!r.ok)throw Error('HTTP '+r.status+' '+(await r.text()));
    const d=await r.json();const names=(llama||strata)?(d.data||[]).map(x=>x.id).filter(x=>typeof x==='string'):(Array.isArray(d.models)?d.models.filter(x=>typeof x==='string'):[]);
    $('modelSelect').replaceChildren(...names.map(name=>new Option(name,name)));
    if(names.length){if(!names.includes(v('model')))$('model').value=names[0];$('modelSelect').value=v('model');}else{$('modelSelect').add(new Option('Нет доступных моделей',''));}
    h.textContent=(d.warning ? d.warning+' · ' : '')+'Найдено моделей: '+names.length;if(v('analystProvider')===v('provider'))setAnalystModels(names);
  }catch(e){$('modelSelect').replaceChildren(new Option('STRATA: список недоступен', ''));$('model').value='';h.textContent='Список моделей недоступен: '+e.message+' · ID можно ввести вручную'}
}
$('modelSelect').addEventListener('change',()=>{if($('modelSelect').value)$('model').value=$('modelSelect').value;refreshLlamaStatus();refreshStrataStatus()});$('model').addEventListener('input',()=>{if(v('provider')==='llamacpp')refreshLlamaStatus()});$('refreshModels').addEventListener('click',refreshModels);
let previousNonStrataContext=null;
$('provider').addEventListener('change',()=>{
  if(v('provider')==='strata'){
    if(previousNonStrataContext===null)previousNonStrataContext=v('ctx');
    $('ctx').value='131072';
  }else if(previousNonStrataContext!==null){
    $('ctx').value=previousNonStrataContext;previousNonStrataContext=null;
  }
  updateLlamaVisibility();
  updateStrataVisibility();
  if(v('provider')!=='ollama'&&v('provider')!=='llamacpp'&&v('provider')!=='strata'){
    $('modelSelect').hidden=true;$('model').hidden=false;$('refreshModels').disabled=true;
    if(v('provider')==='openai' && v('model').startsWith('qwen'))$('model').value='gpt-5.4-mini';
    $('modelHint').textContent='Введи ID модели, загруженной на выбранном API-сервере';
  }else{
    $('modelSelect').hidden=false;$('model').hidden=true;$('refreshModels').disabled=false;
    if(v('model').startsWith('gpt-'))$('model').value='';
    $('refreshModels').textContent=v('provider')==='llamacpp'?'↻ Модели llama.cpp':(v('provider')==='strata'?'↻ Модели STRATA':'↻ Модели Ollama');refreshModels();
  }
});
refreshModels();
$('http').addEventListener('change',()=>{$('httpOpts').hidden=!$('http').checked});
async function loadToken(){const r=await fetch('/api/token');secret=(await r.json()).token}
async function action(path,body){if(!secret)await loadToken();const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json','X-Dexster-Token':secret},body:JSON.stringify(body||{})});if(!r.ok)throw Error(await r.text())}
let reviewToken='';
$('reviewBtn').onclick=async()=>{
 try{
  if(!secret)await loadToken();$('reviewMessage').textContent='Чтение Worker...';
  const r=await fetch('/api/review',{headers:{'X-Dexster-Token':secret},cache:'no-store'});
  if(!r.ok)throw Error(await r.text());const j=await r.json();
  reviewToken=j.token;$('reviewDiff').textContent=(j.files||[]).map(f=>'['+f.kind+'] '+f.path+'\n'+f.diff).join('\n')||'Изменений нет';
  $('reviewMessage').textContent=j.detail;$('applyBtn').disabled=!(j.files||[]).length;
 }catch(e){reviewToken='';$('applyBtn').disabled=true;$('reviewMessage').textContent='REVIEW: '+e.message}
};
$('applyBtn').onclick=async()=>{
 if(!reviewToken)return;
 if(!confirm('Применить показанные изменения к исходному проекту? Оригинальные файлы изменятся.'))return;
 try{
  await action('/api/review/apply',{token:reviewToken});
  $('reviewMessage').textContent='Изменения перенесены в исходный проект';
  reviewToken='';$('applyBtn').disabled=true;
 }catch(e){reviewToken='';$('applyBtn').disabled=true;$('reviewMessage').textContent='APPLY BLOCKED: '+e.message}
};
$('workerProbe').onclick=async()=>{try{if(!secret)await loadToken();$('workerProbeResult').textContent='Проверка...';const r=await fetch('/api/worker/probe',{method:'POST',headers:{'X-Dexster-Token':secret}});const j=await r.json();$('workerProbeResult').textContent=j.detail||j.error||('HTTP '+r.status)}catch(e){$('workerProbeResult').textContent='Worker: '+e.message}};
$('start').onclick=async()=>{try{$('start').disabled=true;$('msg').textContent='Запуск...';if($('analystToggle').checked){$('msg').textContent='Планирование перед запуском...';await analyzeTask();}await action('/api/start',{Runtime:v('runtime'),Provider:v('provider'),StrataURL:v('strataURL'),Root:v('root'),Kraken:v('kraken'),Model:v('model'),Task:v('task'),Analyst:$('analystToggle').checked,AnalystModel:v('analystModel'),AnalystProvider:v('analystProvider'),AnalystPlanID:preparedPlan,Context:n('ctx'),Budget:n('budget'),RotateAt:n('rotate'),MaxIterations:n('iterations'),AutoCommit:$('commit').checked,AutoVerify:$('verify').checked,Unload:v('provider')==='strata'?false:$('unload').checked,HTTPVerify:$('http').checked,HTTPURL:v('url'),HTTPContains:v('contains')});$('msg').textContent='Задача запущена';await poll()}catch(e){$('msg').textContent=e.message}finally{if(!running)$('start').disabled=false}};
$('stop').onclick=async()=>{try{await action('/api/stop');$('msg').textContent='Остановка отправлена'}catch(e){$('msg').textContent=e.message}};
let llamaBusy=false;
function updateLlamaVisibility(){const on=v('provider')==='llamacpp';$('llamaManage').hidden=!on;if(on)refreshLlamaStatus()}
async function refreshLlamaStatus(){
 if(v('provider')!=='llamacpp'||llamaBusy)return;
 const model=v('model').trim(),dot=$('llamaDot'),label=$('llamaStatus');
 dot.style.background='#6a7787';dot.style.boxShadow='none';label.textContent='Проверка...';
 $('llamaLoad').disabled=true;$('llamaUnload').disabled=true;
 if(!model){label.textContent='Введи модель';return}
 try{
  if(!secret)await loadToken();
  const r=await fetch('/api/llamacpp/models',{headers:{'X-Dexster-Token':secret},cache:'no-store'});
  if(!r.ok)throw Error((await r.text()).trim());
  const data=await r.json();const item=(data.data||[]).find(x=>x.id===model);
  const status=item?.status?.value||'not-found';label.textContent=status==='loaded'?'ЗАГРУЖЕНА':status==='loading'?'ЗАГРУЗКА':status==='downloading'?'СКАЧИВАНИЕ':status==='incomplete'?'НЕ ДОКАЧАНА':status==='unloaded'?'Выгружена':status==='sleeping'?'Спит':status==='not-found'?'Нет в кеше':status;
  if(status==='downloading'&&item?.status?.progress){const parts=Object.values(item.status.progress);const done=parts.reduce((a,x)=>a+(Number(x.done)||0),0),total=parts.reduce((a,x)=>a+(Number(x.total)||0),0);if(total>0)label.textContent+=' '+Math.round(done/total*100)+'%';}
  if(status==='loaded'){dot.style.background='#36f68d';dot.style.boxShadow='0 0 6px #36f68d,0 0 17px #36f68d'}
  else if(status==='loading'){dot.style.background='#ffbf54';dot.style.boxShadow='0 0 10px #ffbf54'}
  $('llamaLoad').disabled=running||status==='loaded'||status==='loading'||status==='not-found';
  $('llamaUnload').disabled=running||!(status==='loaded'||status==='sleeping');
 }catch(e){label.textContent='API недоступен';label.title=e.message}
}
async function llamaModelAction(op){
 const model=v('model').trim();if(!model)return;
 llamaBusy=true;$('llamaLoad').disabled=true;$('llamaUnload').disabled=true;$('llamaStatus').textContent=op==='load'?'Загрузка...':'Выгрузка...';
 try{await action('/api/llamacpp/models/'+op,{model});$('msg').textContent='llama.cpp: команда '+(op==='load'?'загрузки':'выгрузки')+' отправлена'}
 catch(e){$('msg').textContent='llama.cpp: '+e.message}
 finally{llamaBusy=false;await refreshLlamaStatus()}
}
$('llamaDownload').onclick=async()=>{const model=prompt('Hugging Face GGUF (repo:quant):',v('model'));if(!model)return;try{await action('/api/llamacpp/models/download',{model});$('msg').textContent='Скачивание запущено: '+model;await refreshModels()}catch(e){$('msg').textContent='Скачивание: '+e.message}};
$('llamaLoad').onclick=()=>llamaModelAction('load');$('llamaUnload').onclick=()=>llamaModelAction('unload');
let strataBusy=false;
function updateStrataVisibility(){
 const on=v('provider')==='strata';
 $('strataOptions').hidden=!on&&v('analystProvider')!=='strata';
 $('strataManage').hidden=!on;
 $('strataManage').style.display=on?'inline-flex':'none';
 $('unloadRow').style.display=on?'none':'';
 $('unload').disabled=on;
 if(on){$('unload').checked=false;refreshStrataStatus();}
}
async function refreshStrataStatus(){
 if(v('provider')!=='strata'||strataBusy)return;
 const dot=$('strataDot'),label=$('strataStatus');
 dot.style.background='#6a7787';dot.style.boxShadow='none';
 $('strataLoad').disabled=true;$('strataUnload').disabled=true;
 try{
  if(!secret)await loadToken();
  const r=await fetch('/api/strata/status?url='+encodeURIComponent(v('strataURL')),{headers:{'X-Dexster-Token':secret},cache:'no-store'});
  if(!r.ok)throw Error((await r.text()).trim());
  const data=await r.json();
  if(typeof data.loaded!=='boolean')throw Error('Неизвестный статус модели');
  label.textContent=data.loaded?'ЗАГРУЖЕНА':(data.online===false?'API НЕ ЗАПУЩЕН · ЗАГРУЗИТЬ':'ВЫГРУЖЕНА');label.title=data.detail||'';
  dot.style.background=data.loaded?'#36f68d':'#6a7787';
  if(data.loaded)dot.style.boxShadow='0 0 8px #36f68d';
  $('strataLoad').disabled=running||data.loaded;
  $('strataUnload').disabled=running||!data.loaded;
 }catch(e){label.textContent='НЕ ЗАПУЩЕНА';label.title=e.message;$('strataLoad').disabled=running;}
}
async function strataModelAction(op){
 const model=v('model').trim();if(!model){$('msg').textContent='STRATA: выбери модель';return;}
 if(running){$('msg').textContent='STRATA: дождись завершения задачи';return;}
 if(op==='unload'&&!confirm('Выгрузить STRATA вручную? Повторная загрузка IQ3_S может занять около 200 секунд.'))return;
 strataBusy=true;$('strataLoad').disabled=true;$('strataUnload').disabled=true;
 $('strataStatus').textContent=op==='load'?'ЗАГРУЗКА (может занять ~200 сек)...':'ВЫГРУЗКА...';
 try{
  await action('/api/strata/models/'+op,{url:v('strataURL'),model});
  $('msg').textContent='STRATA: '+(op==='load'?'модель загружена':'модель выгружена вручную');
 }catch(e){$('msg').textContent='STRATA: '+e.message;}
 finally{strataBusy=false;await refreshStrataStatus();}
}
$('strataLoad').onclick=()=>strataModelAction('load');
$('strataUnload').onclick=()=>strataModelAction('unload');
$('strataURL').addEventListener('change',()=>{clearAnalystPlan();if(v('provider')==='strata'){refreshModels();refreshStrataStatus();}if(v('analystProvider')==='strata')$('analystProvider').dispatchEvent(new Event('change'))});
updateStrataVisibility();updateLlamaVisibility();setInterval(refreshLlamaStatus,3000);setInterval(refreshStrataStatus,5000);
$('analystProvider').addEventListener('change',async()=>{
  clearAnalystPlan();updateStrataVisibility();$('analystModel').value='';if(!secret)await loadToken();
  try{
    const provider=v('analystProvider'),llama=provider==='llamacpp',strata=provider==='strata';
    const path=strata?'/api/strata/models?url='+encodeURIComponent(v('strataURL')):(llama?'/api/llamacpp/models':'/api/models');
    const r=await fetch(path,{headers:(llama||strata)?{'X-Dexster-Token':secret}:{},cache:'no-store'});
    if(!r.ok)throw Error('HTTP '+r.status+' '+(await r.text()));
    const data=await r.json();setAnalystModels((llama||strata)?(data.data||[]).map(x=>x.id):(data.models||[]));
  }catch(e){$('analystModelHint').textContent=e.message}
});

async function updateMemory(){try{const r=await fetch('/api/kraken',{cache:'no-store'});if(!r.ok)return;const k=await r.json();$('memoryRoot').textContent=k.root||'KRAKEN ещё не выбран';$('evCount').textContent=k.counts?.events??0;$('cpCount').textContent=k.counts?.checkpoints??0;$('decCount').textContent=k.counts?.decisions??0;$('memoryInfo').textContent=k.error||('Последние события: '+(k.recent?.events||[]).join(', '));}catch(_){}}
async function poll(){try{const r=await fetch('/api/state');const s=await r.json();running=['running','stopping'].includes(s.status);$('status').textContent=s.status.toUpperCase();$('start').disabled=running;$('stop').disabled=!running;if(s.log!==lastLog){const box=$('log');const atBottom=box.scrollTop+box.clientHeight>=box.scrollHeight-60;box.textContent=s.log||'Ожидание задачи...';if(atBottom)box.scrollTop=box.scrollHeight;lastLog=s.log}if(s.model_log!==lastModelLog){const box=$('modelLog');const atBottom=box.scrollTop+box.clientHeight>=box.scrollHeight-60;box.textContent=liveModelPreview(s.model_log||'Ответы модели появятся после запуска...');if(atBottom)box.scrollTop=box.scrollHeight;lastModelLog=s.model_log}const use=[...(s.log||'').matchAll(/LLM USAGE calls=(\d+) prompt_tokens=(\d+) generated_tokens=(\d+) total_tokens=(\d+)/g)].pop();if(use){$('calls').textContent=use[1];$('tokens').textContent=Number(use[4]).toLocaleString('ru')}const it=[...(s.log||'').matchAll(/\[DEXSTER\] #(\d+)/g)].pop();if(it)$('iter').textContent=it[1];$('meta').textContent='PID: '+(s.pid||'—')+' · '+s.status+' · exit: '+s.exit_code;await updateMemory()}catch(e){$('msg').textContent='Связь с сервером: '+e.message}}
loadToken().then(poll);setInterval(poll,1200);
</script></body></html>`

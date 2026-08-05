package httpapi

// adminTemplates holds the server-rendered admin panel (login + dashboard).
// It is deliberately plain and self-contained (no external assets) so it works
// behind the reverse proxy and secret path without extra wiring.
const adminTemplates = `
{{define "login"}}<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>podkop-server</title>
<style>
 body{font-family:system-ui,Segoe UI,Roboto,sans-serif;background:#0f1115;color:#e6e6e6;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
 form{background:#171a21;padding:32px;border-radius:12px;width:320px;box-shadow:0 8px 30px rgba(0,0,0,.4)}
 h1{font-size:18px;margin:0 0 20px}
 label{display:block;font-size:13px;margin:14px 0 4px;color:#9aa4b2}
 input{width:100%;box-sizing:border-box;padding:10px;border-radius:8px;border:1px solid #2a2f3a;background:#0f1115;color:#e6e6e6}
 button{margin-top:20px;width:100%;padding:11px;border:0;border-radius:8px;background:#3b82f6;color:#fff;font-weight:600;cursor:pointer}
 .err{background:#3a1620;color:#ff9db0;padding:10px;border-radius:8px;font-size:13px;margin-bottom:8px}
</style></head><body>
<form method="post" action="{{.AdminPath}}login">
 <h1>podkop-server</h1>
 {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
 <label>Логин</label><input name="username" autocomplete="username">
 <label>Пароль</label><input name="password" type="password" autocomplete="current-password">
 <button type="submit">Войти</button>
</form></body></html>{{end}}

{{define "dashboard"}}<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>podkop-server — панель</title>
<style>
 body{font-family:system-ui,Segoe UI,Roboto,sans-serif;background:#0f1115;color:#e6e6e6;margin:0;padding:24px}
 h1{font-size:20px}h2{font-size:16px;margin-top:32px;border-bottom:1px solid #2a2f3a;padding-bottom:8px}
 a{color:#60a5fa}
 .bar{display:flex;justify-content:space-between;align-items:center}
 .notice{background:#122a1c;color:#8ff0b5;padding:10px 14px;border-radius:8px;margin:12px 0;word-break:break-all}
 table{width:100%;border-collapse:collapse;margin-top:12px;font-size:13px}
 th,td{text-align:left;padding:8px;border-bottom:1px solid #222835;vertical-align:top}
 code{background:#0b0d11;padding:2px 6px;border-radius:6px;word-break:break-all}
 form.inline{display:inline}
 input,select,textarea{box-sizing:border-box;padding:8px;border-radius:8px;border:1px solid #2a2f3a;background:#0f1115;color:#e6e6e6}
 textarea{width:100%;min-height:60px;font-family:ui-monospace,monospace}
 .card{background:#171a21;padding:16px;border-radius:12px;margin:12px 0}
 .grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}
 button{padding:8px 12px;border:0;border-radius:8px;background:#3b82f6;color:#fff;cursor:pointer}
 button.danger{background:#b3283b}button.ghost{background:#2a2f3a}
 label{font-size:12px;color:#9aa4b2;display:block;margin:8px 0 3px}
</style></head><body>
<div class="bar"><h1>podkop-server</h1><a href="{{.AdminPath}}logout">Выйти</a></div>
{{if .Notice}}<div class="notice">{{.Notice}}</div>{{end}}

<h2>Клиенты (роутеры)</h2>
<table>
 <tr><th>Имя</th><th>Профиль</th><th>Статус</th><th>Ключ / подписка</th><th></th></tr>
 {{range .Clients}}
 <tr>
  <td>{{.Name}}</td>
  <td>{{.ProfileName}}</td>
  <td>{{if .Enabled}}вкл{{else}}выкл{{end}}</td>
  <td><code>{{.Token}}</code><br><small><a href="{{.SubURL}}">{{.SubURL}}</a></small></td>
  <td>
   <form class="inline" method="post" action="{{$.AdminPath}}clients/toggle"><input type="hidden" name="token" value="{{.Token}}"><button class="ghost">{{if .Enabled}}Откл{{else}}Вкл{{end}}</button></form>
   <form class="inline" method="post" action="{{$.AdminPath}}clients/delete" onsubmit="return confirm('Удалить клиента?')"><input type="hidden" name="token" value="{{.Token}}"><button class="danger">Удалить</button></form>
  </td>
 </tr>
 {{else}}<tr><td colspan="5">Пока нет клиентов</td></tr>{{end}}
</table>

<div class="card">
 <h3>Выдать ключ</h3>
 <form method="post" action="{{.AdminPath}}clients/create">
  <div class="grid">
   <div><label>Имя роутера</label><input name="name" required></div>
   <div><label>Профиль маршрутов</label>
    <select name="profile_id">{{range .Profiles}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></div>
  </div>
  <label>Источник ключа</label>
  <label><input type="radio" name="mode" value="manual" checked> Вставить ссылку вручную</label>
  {{if .XUIEnabled}}<label><input type="radio" name="mode" value="xui"> Создать через 3x-UI</label>{{end}}
  <label>Ссылка прокси (vless://…, ss://… — для ручного режима)</label>
  <textarea name="proxy_string" placeholder="vless://uuid@host:443?...#name"></textarea>
  <button type="submit">Выдать</button>
 </form>
</div>

<h2>Профили маршрутов</h2>
{{range .Profiles}}
<div class="card">
 <form method="post" action="{{$.AdminPath}}profiles/save">
  <input type="hidden" name="id" value="{{.ID}}">
  <div class="grid">
   <div><label>Название</label><input name="name" value="{{.Name}}" required></div>
   <div><label>Интервал обновления</label>
    <select name="update_interval">
     {{$u := .UpdateInterval}}
     <option value="1h" {{if eq $u "1h"}}selected{{end}}>1h</option>
     <option value="3h" {{if eq $u "3h"}}selected{{end}}>3h</option>
     <option value="12h" {{if eq $u "12h"}}selected{{end}}>12h</option>
     <option value="1d" {{if eq $u "1d"}}selected{{end}}>1d</option>
     <option value="3d" {{if eq $u "3d"}}selected{{end}}>3d</option>
    </select></div>
  </div>
  <label>Community-списки (через запятую)</label>
  <input name="community_lists" value="{{join .CommunityLists ", "}}">
  <div class="grid">
   <div><label>DNS тип</label>
    <select name="dns_type">
     {{$t := .DNSType}}
     <option value="doh" {{if eq $t "doh"}}selected{{end}}>doh</option>
     <option value="dot" {{if eq $t "dot"}}selected{{end}}>dot</option>
     <option value="udp" {{if eq $t "udp"}}selected{{end}}>udp</option>
    </select></div>
   <div><label>DNS сервер</label><input name="dns_server" value="{{.DNSServer}}"></div>
  </div>
  <label><input type="checkbox" name="p2p_direct" {{if .P2PDirect}}checked{{end}}> P2P напрямую</label>
  <label><input type="checkbox" name="direct_ru_zones" {{if .DirectRUZones}}checked{{end}}> Зоны .ru/.su/.рф напрямую</label>
  <div style="margin-top:12px">
   <button type="submit">Сохранить</button>
  </div>
 </form>
 <form method="post" action="{{$.AdminPath}}profiles/delete" onsubmit="return confirm('Удалить профиль?')" style="margin-top:6px">
  <input type="hidden" name="id" value="{{.ID}}"><button class="danger">Удалить профиль</button>
 </form>
</div>
{{end}}

<div class="card">
 <h3>Новый профиль</h3>
 <form method="post" action="{{.AdminPath}}profiles/save">
  <div class="grid">
   <div><label>Название</label><input name="name" required></div>
   <div><label>Интервал обновления</label>
    <select name="update_interval">
     <option value="1h">1h</option><option value="3h">3h</option><option value="12h">12h</option>
     <option value="1d" selected>1d</option><option value="3d">3d</option>
    </select></div>
  </div>
  <label>Community-списки (через запятую)</label>
  <input name="community_lists" value="russia_inside" placeholder="russia_inside, meta, youtube">
  <div class="grid">
   <div><label>DNS тип</label><select name="dns_type"><option value="doh" selected>doh</option><option value="dot">dot</option><option value="udp">udp</option></select></div>
   <div><label>DNS сервер</label><input name="dns_server" value="https://dns.google/dns-query"></div>
  </div>
  <label><input type="checkbox" name="p2p_direct" checked> P2P напрямую</label>
  <label><input type="checkbox" name="direct_ru_zones" checked> Зоны .ru/.su/.рф напрямую</label>
  <div style="margin-top:12px"><button type="submit">Создать</button></div>
 </form>
</div>

</body></html>{{end}}
`

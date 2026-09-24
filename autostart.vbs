Set WshShell = CreateObject("WScript.Shell")
' Delayed boot: Explorer + network ready dulu (pola rancang-delayed/9router)
WScript.Sleep 30000

' Direktori script ini (VBScript tidak punya %~dp0 — itu sintaks batch)
Dim scriptDir
scriptDir = Left(WScript.ScriptFullName, InStrRev(WScript.ScriptFullName, "\"))

' ---------- Helper: cek URL hidup (status 200) ----------
Function IsUp(url)
  Dim http
  On Error Resume Next
  IsUp = False
  Set http = CreateObject("MSXML2.XMLHTTP")
  http.open "GET", url, False
  http.setRequestHeader "Cache-Control", "no-cache"
  http.send
  If Err.Number = 0 Then
    If http.status = 200 Then IsUp = True
  End If
  Err.Clear
  On Error GoTo 0
End Function

' Wait helper: poll sampai up atau timeout (ms), interval ms
Sub WaitFor(url, timeoutMs, intervalMs)
  Dim waited
  waited = 0
  Do While Not IsUp(url) And waited < timeoutMs
    WScript.Sleep intervalMs
    waited = waited + intervalMs
  Loop
End Sub

' ---------- 1. opencode serve :4096 (upstream relay WAJIB hidup duluan) ----------
If Not IsUp("http://127.0.0.1:4096/api/health") Then
  ' opencode CLI ada di npm global PATH; pakai shim .cmd biar resolve sendiri
  WshShell.Run "cmd /c cd /d """ & scriptDir & """ && opencode serve --port 4096 --hostname 127.0.0.1 >> oc-serve.log 2>&1", 0, False
  ' Beri waktu cold-start Node/Bun + bind port
  WaitFor "http://127.0.0.1:4096/api/health", 45000, 2000
End If

' ---------- 2. zenproxy :8020 (relay ke :4096) ----------
If Not IsUp("http://127.0.0.1:8020/health") Then
  WshShell.Run "cmd /c cd /d """ & scriptDir & """ && zenproxy.exe -port 8020 -config config.json >> zenproxy.log 2>&1", 0, False
End If
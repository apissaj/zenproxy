Set WshShell = CreateObject("WScript.Shell")
WshShell.Run "cmd /c cd /d ""C:\Users\TUF Gaming A15\zenproxy"" && zenproxy.exe -port 8020 -config config.json >> zenproxy.log 2>&1", 0, False

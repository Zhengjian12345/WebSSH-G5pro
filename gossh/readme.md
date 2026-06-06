
###
```bash
gofmt -w -r 'interface{} -> any' .
```

### SQLite init
```shell
go get github.com/glebarez/sqlite

go get github.com/glebarez/go-sqlite
go get modernc.org/sqlite/lib

```nginx
        location ^~ /myapp/webssh/ {
            proxy_pass http://127.0.0.1:8899;
            proxy_pass_header Server;
            proxy_http_version 1.1; 
            proxy_redirect off;

            #proxy_set_header Host $http_host;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Scheme $scheme;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection "upgrade";
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

            proxy_connect_timeout 60s;
            proxy_read_timeout 120s;
            proxy_send_timeout 120s;
            client_body_timeout 60s;
        }

```

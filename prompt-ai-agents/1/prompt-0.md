saya ingin melakukan pengecekan apakah 'main.go' mampu membuat worker sesuai dengan konsep rancangan pada 'rancangan_membuat_agent.md' jika mampu maka saya ingin diberikan hasil berupa file 'curl-agent-<nama_agent>.md' yang berisi beberapa curl, meliputi curl create dan curl webhook untuk melakukan test ke worker yang dibuat serta curl lainya, ini adalah req untuk llm nya (sesuaikan model dengan kebutuhan) :
```curl-ai.md
curl https://api.cloudflare.com/client/v4/accounts/56667d302adadb8e04093b1aac32017c/ai/run/@cf/meta/llama-3.1-8b-instruct-fast \
  -X POST \
  -H "Authorization: Bearer cfut_KpdkVf3ZkIArmKDoBLQySa2TA4Pb5MXY4ZkXA7Qbe21abbbf" \
  -d '{ "messages": [{ "role": "system", "content": "maksimal 6 kata." }, { "role": "user", "content": "Apa itu kopi?" }]}'
```
model llm :
@cf/meta/llama-3.1-70b-instruct
@cf/meta/llama-3.1-8b-instruct-fast
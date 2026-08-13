import http from 'follow-redirects/http';

await import('axios');

http.request({ host: 'example.com' });

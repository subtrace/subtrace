-- Part 1 simulates the backend server of a product called gptwrapper that lets
-- users chat with a PDF file by using the OpenAI finetuning API underneath.
--
-- Part 2 simulates a user loading a web page on their browser with request
-- correlation between the client and the server.

INSERT INTO subtrace_events_{{.Suffix}} (time, event_id, service, hostname, process_id, tls_server_name, http_version, http_is_outgoing, http_client_addr, http_server_addr, http_duration, http_req_method, http_req_path, http_resp_status_code) VALUES
  (now64(9) + toIntervalSecond(0.000), 'c5e83f39-a901-4f64-8d88-e04a72e711d1', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:39398',   '127.0.0.1:443',      198_532_566, 'PUT',    '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a',      '201'),
  (now64(9) + toIntervalSecond(0.100), '43f1d913-101c-4ae8-9980-e2bf653dc1b7', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   104_563_859, 'GET',    '/v1/models',                                              '401'),
  (now64(9) + toIntervalSecond(0.200), '787ff1b8-2a72-4b75-9145-7122348696f7', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   214_360_730, 'GET',    '/v1/models',                                              '200'),
  (now64(9) + toIntervalSecond(0.300), 'b977f906-f021-4053-be69-87aa591a44b3', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:49398', '104.18.7.192:443',   894_670_200, 'GET',    '/v1/files',                                               '200'),
  (now64(9) + toIntervalSecond(0.400), 'bf837b20-5b9d-4cb9-843b-c4da604d77d3', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 2_180_656_570, 'POST',   '/v1/files',                                               '200'),
  (now64(9) + toIntervalSecond(0.500), '9f0195f6-03d4-4843-8289-0cfa525a753a', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:39398',   '127.0.0.1:443',      198_532_566, 'GET',    '/api/pdf-chat/bc7bf1a6-a3ce-4f95-a4fb-be81d88a123f',      '403'),
  (now64(9) + toIntervalSecond(0.600), '7e4b9c77-2dc5-4dd2-bb3c-3df37b7a0124', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:39398',   '127.0.0.1:443',      198_532_566, 'GET',    '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a',      '200'),
  (now64(9) + toIntervalSecond(0.700), 'a908d1ec-65f3-489c-bf27-2f891ed8513d', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   803_620_010, 'GET',    '/v1/files',                                               '200'),
  (now64(9) + toIntervalSecond(0.800), '0e5027e7-78e6-4d5a-9350-ca7865597734', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   424_549_427, 'GET',    '/v1/files/file-abc123',                                   '201'),
  (now64(9) + toIntervalSecond(0.900), '7454fd4f-c825-4dda-8258-ccf8932f2d45', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 1_382_782_928, 'GET',    '/v1/files/file-xyz789',                                   '404'),
  (now64(9) + toIntervalSecond(1.000), '571c82fb-7bef-4eb6-9b4d-0f021a237993', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:49398', '104.18.7.192:443',   989_862_223, 'POST',   '/v1/fine_tuning/jobs',                                    '201'),
  (now64(9) + toIntervalSecond(1.100), 'a7bdaa14-cce9-49c8-bda3-b34839597d35', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   204_171_672, 'GET',    '/v1/fine_tuning/jobs/ftjob-abc123',                       '200'),
  (now64(9) + toIntervalSecond(1.200), 'd5168899-b030-4d91-83d0-df35164ec8ba', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   166_103_541, 'GET',    '/v1/fine_tuning/jobs/ftjob-abc123',                       '200'),
  (now64(9) + toIntervalSecond(1.300), 'd730bf34-1f7d-41c2-82b3-ad50cc006971', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:36668',   '127.0.0.1:443',      361_927_198, 'GET',    '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a',      '200'),
  (now64(9) + toIntervalSecond(1.400), '5a29a200-a21c-45db-bc28-e699e195f827', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   283_745_489, 'GET',    '/v1/fine_tuning/jobs/ftjob-abc123',                       '200'),
  (now64(9) + toIntervalSecond(1.500), 'eaa74173-ca54-4cf2-b967-a81b77d4efb9', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:49398', '104.18.7.192:443',   273_540_397, 'GET',    '/v1/fine_tuning/jobs/ftjob-abc123',                       '200'),
  (now64(9) + toIntervalSecond(1.600), '5e3b0a07-ee59-4baf-b934-b688a204b423', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 1_226_892_475, 'DELETE', '/v1/files/file-abc123',                                   '200'),
  (now64(9) + toIntervalSecond(1.700), 'bf9641fb-7a3f-478f-96fc-968672b156fa', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   226_375_513, 'GET',    '/v1/models',                                              '200'),
  (now64(9) + toIntervalSecond(1.800), '6a37f0bf-c8e5-4d0c-9651-7c71df736156', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',      460_369_361, 'GET',    '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a',      '302'),
  (now64(9) + toIntervalSecond(1.900), 'f583c941-e2e0-4f4b-8340-f31320dde8b9', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',      212_668_720, 'GET',    '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '200'),
  (now64(9) + toIntervalSecond(2.000), 'dafed451-e5cd-4396-ae72-5a036b6eca3a', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',    3_682_401_533, 'POST',   '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '200'),
  (now64(9) + toIntervalSecond(2.100), 'a50aaee1-f245-4b30-a8a9-74c11626cfcb', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 3_665_241_279, 'POST',   '/v1/chat/completions',                                    '200'),
  (now64(9) + toIntervalSecond(2.200), '12268d29-a155-40b2-b568-e354d73b4ad7', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',    4_211_401_533, 'POST',   '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '200'),
  (now64(9) + toIntervalSecond(2.300), '022d16c6-fde2-47f3-9bf2-6ca6c95e78d1', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 4_147_831_022, 'POST',   '/v1/chat/completions',                                    '200'),
  (now64(9) + toIntervalSecond(2.400), 'b729c04f-e41c-4f1d-9c15-454ff868786a', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',    4_493_401_533, 'POST',   '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '200'),
  (now64(9) + toIntervalSecond(2.500), 'a7670334-1941-409d-8a50-4367ce9e62fe', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 4_481_724_652, 'POST',   '/v1/chat/completions',                                    '200'),
  (now64(9) + toIntervalSecond(2.600), '907b2cf1-9c43-469d-834b-70831def04d7', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',    4_773_401_533, 'POST',   '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '200'),
  (now64(9) + toIntervalSecond(2.700), 'd02efe83-cd9e-4376-b75f-fae301db7f18', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 4_781_724_652, 'POST',   '/v1/chat/completions',                                    '200'),
  (now64(9) + toIntervalSecond(2.800), 'f65e3466-afbd-469b-912c-36cc15a7406b', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',    4_992_401_533, 'POST',   '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '200'),
  (now64(9) + toIntervalSecond(2.900), 'ded7bb42-ef40-4095-80da-ec59a0770b18', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443', 4_973_540_397, 'POST',   '/v1/chat/completions',                                    '200'),
  (now64(9) + toIntervalSecond(3.000), 'fd35952a-4533-4f10-a9ee-bc7c919f65cb', 'gptwrapper', 'prod-us-east-1-001', '116086', 'gptwrapper.ai',    'HTTP/1.1', 'false', '1.2.3.4:33118',   '127.0.0.1:443',      478_401_533, 'POST',   '/api/pdf-chat/40b7f609-5653-462f-88c7-7069c8d1fb4a/chat', '429'),
  (now64(9) + toIntervalSecond(3.100), '151baee8-bc9d-442d-b1ff-9bf51d96524c', 'gptwrapper', 'prod-us-east-1-001', '116086', 'api.openai.com',   'HTTP/1.1', 'true',  '127.0.0.1:40402', '104.18.6.192:443',   466_919_771, 'POST',   '/v1/chat/completions',                                    '429'),

  (now64(9) + toIntervalSecond(1.000), '9d6aeb7e-b81e-45c1-b768-3ad783ebb0d1', 'subtrace',  '16021', 'Users-MacBook-Air',          'subtrace.dev', 'HTTP/1.1', 'true',  '127.1.1.1:58167', '5.6.7.8:443',    21_112_868, 'HEAD', '/index.html',           '200'),
  (now64(9) + toIntervalSecond(1.100), '1a9451f8-426b-49bc-9620-c41f466b3135', 'subtrace', '316021', 'dashboard-server-us-east-1', 'subtrace.dev', 'HTTP/1.1', 'false', '1.2.3.4:58167',   '127.1.1.1:443',   5_045_126, 'HEAD', '/index.html',           '200'),
  (now64(9) + toIntervalSecond(1.200), '33da3988-7d3c-4f5b-be80-82e1f81fe7c3', 'subtrace',  '16021', 'Users-MacBook-Air',          'subtrace.dev', 'HTTP/1.1', 'true',  '127.1.1.1:58167', '5.6.7.8:443',    19_153_175, 'GET',  '/index.html',           '200'),
  (now64(9) + toIntervalSecond(1.300), '70e474b4-0c59-4433-b7ea-f3f96dc8675a', 'subtrace', '316021', 'dashboard-server-us-east-1', 'subtrace.dev', 'HTTP/1.1', 'false', '1.2.3.4:58167',   '127.1.1.1:443',   6_393_754, 'GET',  '/index.html',           '200'),
  (now64(9) + toIntervalSecond(1.400), '0c1cbe6d-9a82-4648-a89d-86d70004b95b', 'subtrace',  '16025', 'Users-MacBook-Air',          'subtrace.dev', 'HTTP/1.1', 'true',  '127.1.1.1:57114', '5.6.7.8:443',    37_549_338, 'GET',  '/style.css',            '200'),
  (now64(9) + toIntervalSecond(1.500), '3771e308-1617-41d4-913c-ca72471725f6', 'subtrace', '316021', 'dashboard-server-us-east-1', 'subtrace.dev', 'HTTP/1.1', 'false', '1.2.3.4:57114',   '127.1.1.1:443',  24_892_468, 'GET',  '/style.css',            '200'),
  (now64(9) + toIntervalSecond(1.600), '71b11b38-c4bf-4af6-b327-1d1c4d7e5bca', 'subtrace',  '16029', 'Users-MacBook-Air',          'subtrace.dev', 'HTTP/1.1', 'true',  '127.1.1.1:57209', '5.6.7.8:443',   112_680_808, 'GET',  '/bundle.3eb2bc9b5c.js', '200'),
  (now64(9) + toIntervalSecond(1.700), '5d03c131-3e0b-4aeb-b6e6-2aa99a766294', 'subtrace', '316021', 'dashboard-server-us-east-1', 'subtrace.dev', 'HTTP/1.1', 'false', '1.2.3.4:57209',   '127.1.1.1:443',  99_528_802, 'GET',  '/bundle.3eb2bc9b5c.js', '200'),
  (now64(9) + toIntervalSecond(1.800), '71b11b38-c4bf-4af6-b327-1d1c4d7e5bca', 'subtrace',  '16021', 'Users-MacBook-Air',          'subtrace.dev', 'HTTP/1.1', 'true',  '127.1.1.1:57209', '5.6.7.8:443',    23_263_393, 'GET',  '/favicon.ico',          '404'),
  (now64(9) + toIntervalSecond(1.900), '415f286d-106f-45e4-a542-4aed1a0f2d66', 'subtrace', '316021', 'dashboard-server-us-east-1', 'subtrace.dev', 'HTTP/1.1', 'false', '1.2.3.4:57209',   '127.1.1.1:443',  10_443_851, 'GET',  '/favicon.ico',          '404');

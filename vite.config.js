import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
export default defineConfig({plugins:[vue()],ssr:{noExternal:["vuetify"]},server:{host:'0.0.0.0',port:4173,allowedHosts:['terminal.local']}});

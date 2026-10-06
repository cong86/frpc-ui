import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({root:'web',plugins:[vue()],build:{outDir:'../internal/server/assets',emptyOutDir:true},server:{host:'127.0.0.1',proxy:{'/api':{target:'http://127.0.0.1:18745',changeOrigin:true,configure(proxy){proxy.on('proxyReq',request=>request.setHeader('Origin','http://127.0.0.1:18745'))}}}}})

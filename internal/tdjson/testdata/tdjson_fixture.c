/* Minimal ABI fixture. Not Telegram and not included in a production image. */
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
static pthread_mutex_t lock=PTHREAD_MUTEX_INITIALIZER;
static char *queue[512]; static int head=0,tail=0,next_id=0; static char *last=NULL;
int td_create_client_id(void){pthread_mutex_lock(&lock);int id=++next_id;pthread_mutex_unlock(&lock);return id;}
void td_send(int id,const char *request){
 char out[2048];
 if(strstr(request,"\"@type\":\"close\"")){
  snprintf(out,sizeof(out),"{\"@client_id\":%d,\"@type\":\"updateAuthorizationState\",\"authorization_state\":{\"@type\":\"authorizationStateClosed\"}}",id);
 }else{
  const char *extra=strstr(request,"\"@extra\":\""); char tag[128]={0};
  if(extra){extra+=10;const char *end=strchr(extra,'"');if(end && end-extra<127)memcpy(tag,extra,end-extra);}
  snprintf(out,sizeof(out),"{\"@client_id\":%d,\"@extra\":\"%s\",\"@type\":\"authorizationStateWaitTdlibParameters\"}",id,tag);
 }
 pthread_mutex_lock(&lock);queue[tail++%512]=strdup(out);pthread_mutex_unlock(&lock);
}
const char *td_receive(double timeout){
 (void)timeout;pthread_mutex_lock(&lock);free(last);last=NULL;
 if(head<tail){last=queue[head++%512];}
 pthread_mutex_unlock(&lock);if(!last)usleep(1000);return last;
}
const char *td_execute(const char *request){(void)request;return "{\"@type\":\"ok\"}";}

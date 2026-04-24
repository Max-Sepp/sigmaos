message(STATUS "Building s3 proto library")

add_library(s3_proto STATIC
  s3.pb.cc
)

target_link_libraries(s3_proto PUBLIC rpc_proto)

target_include_directories(s3_proto PRIVATE
  ${CMAKE_SOURCE_DIR}
  ${Protobuf_INCLUDE_DIRS}
)
